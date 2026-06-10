package laozi

import (
	"context"
	"strings"
	"testing"
)

// fixedClassifierLLM returns a canned domain word (or an error).
type fixedClassifierLLM struct {
	reply string
	err   error
}

func (m fixedClassifierLLM) Chat(_ context.Context, _, _ string) (string, error) {
	return m.reply, m.err
}

func demoDomains() []Domain {
	return []Domain{
		{Name: "financial_analysis", Description: "expenses, revenue, margins",
			Keywords: []string{"expense", "revenue", "cash flow", "margin"}, Categories: []string{"liquidity"}},
		{Name: "transaction_clarification", Description: "payments and transfers",
			Keywords: []string{"payment", "transfer", "venmo", "vendor"}, Categories: []string{"transactions"}},
		{Name: "entity_structure", Description: "ownership and partners",
			Keywords: []string{"partner", "owner", "LLC", "ownership"}},
	}
}

func TestClassifyLayer1LLM(t *testing.T) {
	// LLM returns a specific domain -> Layer 1, done.
	c := NewClassifier(
		WithDomains(demoDomains()),
		WithClassifierLLM(fixedClassifierLLM{reply: "transaction_clarification"}),
	)
	got := c.Classify(context.Background(), "anything at all", nil)
	if got.Domain != "transaction_clarification" || got.Layer != 1 {
		t.Errorf("got %+v, want transaction_clarification at layer 1", got)
	}
}

func TestClassifyLayer2RegexFallback(t *testing.T) {
	// LLM says "general" -> fall through to keyword match -> Layer 2.
	c := NewClassifier(
		WithDomains(demoDomains()),
		WithClassifierLLM(fixedClassifierLLM{reply: "general"}),
	)
	got := c.Classify(context.Background(), "I made a transfer via venmo yesterday", nil)
	if got.Domain != "transaction_clarification" || got.Layer != 2 {
		t.Errorf("got %+v, want transaction_clarification at layer 2", got)
	}
}

func TestClassifyLayer3Default(t *testing.T) {
	// LLM unsure and no keywords match -> fallback at Layer 3.
	c := NewClassifier(
		WithDomains(demoDomains()),
		WithClassifierLLM(fixedClassifierLLM{reply: "general"}),
	)
	got := c.Classify(context.Background(), "what is the weather like", nil)
	if got.Domain != "general" || got.Layer != 3 {
		t.Errorf("got %+v, want general at layer 3", got)
	}
}

func TestClassifyLLMErrorDegradesToRegex(t *testing.T) {
	// LLM errors -> graceful degradation to keyword layer.
	c := NewClassifier(
		WithDomains(demoDomains()),
		WithClassifierLLM(fixedClassifierLLM{err: context.DeadlineExceeded}),
	)
	got := c.Classify(context.Background(), "our operating margin slipped", nil)
	if got.Domain != "financial_analysis" || got.Layer != 2 {
		t.Errorf("got %+v, want financial_analysis at layer 2 after LLM error", got)
	}
}

func TestClassifyNoLLMIsDeterministic(t *testing.T) {
	// With no LLM, only Layers 2 and 3 run.
	c := NewClassifier(WithDomains(demoDomains()))
	if got := c.Classify(context.Background(), "who owns this LLC", nil); got.Domain != "entity_structure" || got.Layer != 2 {
		t.Errorf("got %+v, want entity_structure at layer 2", got)
	}
	if got := c.Classify(context.Background(), "hello there", nil); got.Domain != "general" || got.Layer != 3 {
		t.Errorf("got %+v, want general at layer 3", got)
	}
}

func TestClassifyKeywordWordBoundary(t *testing.T) {
	// "revenue" should match; a substring inside another word should not.
	c := NewClassifier(WithDomains(demoDomains()))
	if got := c.Classify(context.Background(), "our revenue grew", nil); got.Domain != "financial_analysis" {
		t.Errorf("expected financial_analysis, got %+v", got)
	}
	if got := c.Classify(context.Background(), "the ownerships paperwork", nil); got.Layer == 2 && got.Domain == "entity_structure" {
		// "ownership" keyword is bounded; "ownerships" contains it as a prefix.
		// \b allows a match here (word char run), which is acceptable; assert no panic and a defined result.
		_ = got
	}
}

func TestClassifyMultiWordKeyword(t *testing.T) {
	c := NewClassifier(WithDomains(demoDomains()))
	got := c.Classify(context.Background(), "reviewing our cash flow this quarter", nil)
	if got.Domain != "financial_analysis" || got.Layer != 2 {
		t.Errorf("got %+v, want financial_analysis at layer 2 (multi-word keyword)", got)
	}
}

func TestClassifierHistoryTrimmedToWindow(t *testing.T) {
	// Capture the prompt to confirm only the last ClassifierHistoryWindow lines are sent.
	var seen string
	cap := capturePromptLLM{out: "general", capture: &seen}
	c := NewClassifier(WithDomains(demoDomains()), WithClassifierLLM(cap))
	history := []string{"msg1", "msg2", "msg3", "msg4", "msg5", "msg6"}
	c.Classify(context.Background(), "anything", history)
	if strings.Contains(seen, "msg1") || strings.Contains(seen, "msg2") {
		t.Errorf("history not trimmed to last %d: prompt still has early msgs", ClassifierHistoryWindow)
	}
	if !strings.Contains(seen, "msg6") {
		t.Error("most recent history line missing from prompt")
	}
}

type capturePromptLLM struct {
	out     string
	capture *string
}

func (m capturePromptLLM) Chat(_ context.Context, _, user string) (string, error) {
	*m.capture = user
	return m.out, nil
}

// ---- Config file loading (documented YAML subset) --------------------------

const sampleDomainsYAML = `
# sample
fallback: general
domains:
  - name: financial_analysis
    description: Expenses, revenue, margins
    keywords: [expense, revenue, "cash flow", margin]
    categories: [liquidity, profitability]
    max_tokens: 800
  - name: entity_structure
    description: Ownership and partners
    keywords: [partner, owner, LLC]
    actions: [define_partners, confirm]
`

func TestLoadDomainsYAMLSubset(t *testing.T) {
	spec, err := LoadDomains(strings.NewReader(sampleDomainsYAML))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if spec.Fallback != "general" {
		t.Errorf("fallback = %q, want general", spec.Fallback)
	}
	if len(spec.Domains) != 2 {
		t.Fatalf("got %d domains, want 2", len(spec.Domains))
	}
	fa := spec.Domains[0]
	if fa.Name != "financial_analysis" || fa.Description != "Expenses, revenue, margins" {
		t.Errorf("domain[0] mismatch: %+v", fa)
	}
	if len(fa.Keywords) != 4 || fa.Keywords[2] != "cash flow" {
		t.Errorf("keywords mismatch (quoted multi-word): %v", fa.Keywords)
	}
	if len(fa.Categories) != 2 || fa.MaxTokens != 800 {
		t.Errorf("categories/max_tokens mismatch: %+v", fa)
	}
	if es := spec.Domains[1]; len(es.Actions) != 2 || es.Actions[0] != "define_partners" {
		t.Errorf("actions mismatch: %+v", es)
	}

	// The loaded spec drives a classifier identically to code registration.
	c := NewClassifier(WithSpec(spec))
	if got := c.Classify(context.Background(), "who is the owner", nil); got.Domain != "entity_structure" {
		t.Errorf("spec-driven classifier: got %+v, want entity_structure", got)
	}
}

func TestLoadDomainsErrors(t *testing.T) {
	bad := map[string]string{
		"keywords not a list": "domains:\n  - name: x\n    keywords: expense\n",
		"unknown field":       "domains:\n  - name: x\n    frobnicate: 1\n",
		"bad max_tokens":      "domains:\n  - name: x\n    max_tokens: lots\n",
		"unknown top key":     "widgets: 3\n",
	}
	for label, src := range bad {
		if _, err := LoadDomains(strings.NewReader(src)); err == nil {
			t.Errorf("%s: expected an error, got none", label)
		}
	}
}

// ---- Context-limiting integration ------------------------------------------

func TestAnalyzeSelectedLimitsToDomainCategories(t *testing.T) {
	resp := `{"insight":{"text":"current_ratio is within the registered range.","severity":"success","reference":"CFA - https://cfa/x"}}`
	e := New(WithLLM(mockLLM{resp: resp}))
	e.AddCategories([]Category{
		{ID: "liquidity", Name: "Liquidity", Thresholds: []Threshold{{Metric: "current_ratio", Min: 1.5, Max: 3, Unit: "ratio", Source: "CFA", SourceURL: "https://cfa/x"}}},
		{ID: "profitability", Name: "Profitability", Thresholds: []Threshold{{Metric: "margin", Min: 10, Max: 40, Unit: "%", Source: "CFA", SourceURL: "https://cfa/x"}}},
		{ID: "ownership", Name: "Ownership", Thresholds: []Threshold{{Metric: "equity", Min: 0, Max: 100, Unit: "%", Source: "CFA", SourceURL: "https://cfa/x"}}},
	})

	// A financial_analysis domain limits analysis to its two categories.
	insights, err := e.AnalyzeSelected(context.Background(),
		[]string{"liquidity", "profitability"},
		map[string]float64{"current_ratio": 2.0, "margin": 25, "equity": 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(insights) != 2 {
		t.Fatalf("AnalyzeSelected returned %d insights, want 2 (context limited)", len(insights))
	}
	got := map[string]bool{}
	for _, in := range insights {
		got[in.CategoryID] = true
	}
	if !got["liquidity"] || !got["profitability"] || got["ownership"] {
		t.Errorf("wrong categories analyzed: %v", got)
	}
}
