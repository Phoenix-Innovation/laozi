package laozi

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// ============================================================================
// Adaptive Query Classification (input side)
//
// Free-form user input is classified into ONE domain before any context is
// loaded, so only the analytics/categories relevant to that domain are used.
// Domains are fungible — they differ per market — so they are pluggable, either
// in code (this file) or from a config file (classifier_config.go).
//
// The cascade has three layers with progressive fallback:
//   Layer 1  LLM classifier  — low-temperature call; if it returns a specific
//                              domain (not the fallback), classification is done.
//   Layer 2  Keyword regex   — deterministic safety net when the LLM is unsure.
//   Layer 3  Default         — the fallback domain ("general").
//
// The LLM is optional: with no LLM client the classifier runs Layer 2 + 3 only,
// which is fully deterministic and dependency-free.
// ============================================================================

// Domain is a fungible classification target. Name is what the classifier
// resolves to; the remaining fields let a host build a domain-tuned pipeline
// (limited categories, system prompt, valid actions, token budget) per the PRD.
type Domain struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`          // shown to the LLM classifier
	Keywords     []string `json:"keywords,omitempty"`   // Layer 2 regex safety net
	Categories   []string `json:"categories,omitempty"` // category IDs this domain limits analysis to
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Actions      []string `json:"actions,omitempty"`
	MaxTokens    int      `json:"max_tokens,omitempty"`
}

// Classification is the cascade result.
type Classification struct {
	Domain string `json:"domain"`
	Layer  int    `json:"layer"` // 1=LLM, 2=keyword, 3=fallback
	Reason string `json:"reason"`
}

// Classifier routes input to a domain via the three-layer cascade.
type Classifier struct {
	llm      LLMClient
	domains  []Domain
	res      []*regexp.Regexp // compiled keyword matchers, parallel to domains
	fallback string
}

// ClassifierOption configures a Classifier.
type ClassifierOption func(*Classifier)

// WithClassifierLLM sets the Layer-1 LLM. Use a low-temperature client for
// reproducible classification (the Chat interface does not carry temperature).
func WithClassifierLLM(llm LLMClient) ClassifierOption {
	return func(c *Classifier) { c.llm = llm }
}

// WithDomain registers a single domain (code-level plugin).
func WithDomain(d Domain) ClassifierOption {
	return func(c *Classifier) { c.domains = append(c.domains, d) }
}

// WithDomains registers several domains at once.
func WithDomains(ds []Domain) ClassifierOption {
	return func(c *Classifier) { c.domains = append(c.domains, ds...) }
}

// WithFallbackDomain overrides the default fallback domain name ("general").
func WithFallbackDomain(name string) ClassifierOption {
	return func(c *Classifier) { c.fallback = name }
}

// WithSpec registers all domains from a loaded config spec.
func WithSpec(spec DomainSpec) ClassifierOption {
	return func(c *Classifier) {
		c.domains = append(c.domains, spec.Domains...)
		if spec.Fallback != "" {
			c.fallback = spec.Fallback
		}
	}
}

// NewClassifier builds a classifier and compiles each domain's keyword matcher.
func NewClassifier(opts ...ClassifierOption) *Classifier {
	c := &Classifier{fallback: "general"}
	for _, opt := range opts {
		opt(c)
	}
	c.res = make([]*regexp.Regexp, len(c.domains))
	for i, d := range c.domains {
		c.res[i] = compileKeywords(d.Keywords)
	}
	return c
}

// compileKeywords builds a case-insensitive, word-bounded alternation regex.
func compileKeywords(keywords []string) *regexp.Regexp {
	var quoted []string
	for _, k := range keywords {
		k = strings.TrimSpace(k)
		if k != "" {
			quoted = append(quoted, regexp.QuoteMeta(k))
		}
	}
	if len(quoted) == 0 {
		return nil
	}
	return regexp.MustCompile(`(?i)\b(` + strings.Join(quoted, "|") + `)\b`)
}

// Domains returns the registered domains (excluding the implicit fallback).
func (c *Classifier) Domains() []Domain { return c.domains }

// Domain looks up a registered domain by name.
func (c *Classifier) Domain(name string) (Domain, bool) {
	for _, d := range c.domains {
		if d.Name == name {
			return d, true
		}
	}
	return Domain{}, false
}

// Classify runs the cascade. history is recent conversation lines (oldest
// first); only the last ClassifierHistoryWindow are sent to the LLM.
func (c *Classifier) Classify(ctx context.Context, message string, history []string) Classification {
	// Layer 1: LLM classifier (optional).
	if c.llm != nil {
		if name, ok := c.llmClassify(ctx, message, history); ok {
			return Classification{Domain: name, Layer: 1, Reason: "LLM classifier"}
		}
		// LLM errored or returned the fallback/unknown -> fall through.
	}
	// Layer 2: keyword regex safety net.
	for i, d := range c.domains {
		if c.res[i] != nil && c.res[i].MatchString(message) {
			return Classification{Domain: d.Name, Layer: 2, Reason: "keyword match"}
		}
	}
	// Layer 3: default fallback.
	return Classification{Domain: c.fallback, Layer: 3, Reason: "default fallback"}
}

// llmClassify asks the LLM for a single domain word. Returns (name, true) only
// when the response matches a registered domain other than the fallback.
func (c *Classifier) llmClassify(ctx context.Context, message string, history []string) (string, bool) {
	if n := len(history); n > ClassifierHistoryWindow {
		history = history[n-ClassifierHistoryWindow:]
	}

	var b strings.Builder
	b.WriteString("Classify the user's message into exactly ONE category.\n\nCategories:\n")
	for _, d := range c.domains {
		fmt.Fprintf(&b, "- %s: %s\n", d.Name, d.Description)
	}
	fmt.Fprintf(&b, "- %s: anything that does not fit the categories above\n\n", c.fallback)
	if len(history) > 0 {
		b.WriteString("Recent conversation:\n")
		for _, h := range history {
			b.WriteString(h)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "User message: %s\n\nReply with ONLY the category word.", message)

	sys := "You are a domain classifier. Respond with exactly one category word from the provided list and nothing else."
	out, err := c.llm.Chat(ctx, sys, b.String())
	if err != nil {
		return "", false
	}
	low := strings.ToLower(out)
	for _, d := range c.domains {
		if strings.Contains(low, strings.ToLower(d.Name)) {
			return d.Name, true
		}
	}
	return "", false // fallback/unknown -> fall through to Layer 2
}

// AnalyzeSelected analyzes only the named categories — the context-limiting step
// once a domain has been classified (typically passing the domain's Categories).
func (e *Engine) AnalyzeSelected(ctx context.Context, categoryIDs []string, metrics map[string]float64) ([]Insight, error) {
	if e.llm == nil {
		return nil, fmt.Errorf("LLM client not configured")
	}

	e.mu.RLock()
	var cats []Category
	for _, id := range categoryIDs {
		if c, ok := e.categories[id]; ok {
			cats = append(cats, c)
		}
	}
	e.mu.RUnlock()

	type result struct {
		insight *Insight
		err     error
	}
	results := make([]result, len(cats))
	sem := make(chan struct{}, e.cfg.MaxParallel)
	var wg sync.WaitGroup
	for i, cat := range cats {
		wg.Add(1)
		go func(i int, cat Category) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ins, err := e.analyzeCategory(ctx, cat, metrics)
			results[i] = result{ins, err}
		}(i, cat)
	}
	wg.Wait()

	var insights []Insight
	for _, r := range results {
		if r.err != nil {
			return nil, r.err
		}
		if r.insight != nil {
			insights = append(insights, *r.insight)
		}
	}
	return insights, nil
}
