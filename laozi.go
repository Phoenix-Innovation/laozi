// Package laozi provides a dual-constraint insight generation engine.
// It prevents LLM hallucinations by combining hardcoded thresholds (Tier 1)
// with optional RAG context (Tier 2), and a post-generation enforcement layer
// that catches and corrects every deviation the LLM produces.
//
// The enforcement layer guarantees:
//   - Severity is always determined by threshold math, not the LLM
//   - Citations trace back to provided source URLs, not invented ones
//   - Suggested goals align with threshold bounds, not hallucinated targets
//   - (Strict mode) Prose containing untraceable numbers is replaced entirely
//
// Usage:
//
//	var client laozi.LLMClient // any type with Chat(ctx, system, user) (string, error)
//
//	engine := laozi.New(
//	        laozi.WithLLM(client),
//	        laozi.WithConfig(laozi.Config{MaxRetries: 2, MaxParallel: 8}),
//	        laozi.WithStrict(true),
//	)
//
//	engine.AddCategory(laozi.Category{
//	        ID: "glucose", Name: "Blood Glucose",
//	        Thresholds: []laozi.Threshold{{
//	                Metric: "fasting_glucose", Min: 70, Max: 99, Unit: "mg/dL",
//	                Source: "ADA", SourceURL: "https://diabetes.org/diagnosis",
//	        }},
//	})
//
//	insights, err := engine.Analyze(ctx, map[string]float64{"fasting_glucose": 112})
package laozi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"
)

// ---------------------------------------------------------------------------
// Core types
// ---------------------------------------------------------------------------

// Severity levels for insights.
type Severity string

const (
	SeverityWarning Severity = "warning" // Value outside threshold
	SeveritySuccess Severity = "success" // Value within threshold
	SeverityInfo    Severity = "info"    // Educational/informational
	// SeverityUnavailable means the required metric(s) were not supplied, so no
	// determination could be made. It is never "success" — a missing input must
	// not read as a passing result.
	SeverityUnavailable Severity = "unavailable"
)

// Threshold defines a constraint for a metric.
type Threshold struct {
	Metric      string  `json:"metric"`
	Expression  string  `json:"expression,omitempty"` // optional Lao Zi DSL; host compiles+runs it to produce Metric's value
	Min         float64 `json:"min"`
	Max         float64 `json:"max"`
	OptimalMin  float64 `json:"optimal_min,omitempty"`
	OptimalMax  float64 `json:"optimal_max,omitempty"`
	Unit        string  `json:"unit"`
	Source      string  `json:"source"`
	SourceURL   string  `json:"source_url"`
	Description string  `json:"description,omitempty"`
	// Optional marks a metric whose absence does NOT make the category
	// unavailable. By default every threshold metric is required: if any
	// required metric is missing or non-finite, the category is Unavailable
	// (never success).
	Optional bool `json:"optional,omitempty"`
}

// Category groups related metrics and thresholds.
type Category struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Thresholds  []Threshold `json:"thresholds"`
	Educational bool        `json:"educational"`
	RAGQuery    string      `json:"rag_query,omitempty"`
	// RequireRAG makes retrieval mandatory: if RAGQuery is set and retrieval
	// fails or returns nothing, the category is Unavailable (fail closed)
	// rather than silently proceeding without the cited evidence.
	RequireRAG bool `json:"require_rag,omitempty"`
	// Version identifies the category definition for the audit trail.
	Version string `json:"version,omitempty"`
}

// Insight is the output from analysis. The Violations slice is the audit
// trail: it records every case where the LLM deviated from deterministic
// truth and was corrected. An empty slice means the LLM was fully compliant.
type Insight struct {
	ID             string            `json:"id"`
	CategoryID     string            `json:"category_id"`
	CategoryName   string            `json:"category_name"`
	Text           string            `json:"text"`
	Severity       Severity          `json:"severity"`
	RelatedMetrics []string          `json:"related_metrics"`
	Reference      string            `json:"reference"`
	SuggestedGoal  *SuggestedGoal    `json:"suggested_goal,omitempty"`
	Violations     []Violation       `json:"violations,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

// SuggestedGoal provides actionable targets.
type SuggestedGoal struct {
	Metric      string  `json:"metric"`
	Target      float64 `json:"target"`
	Unit        string  `json:"unit"`
	Comparison  string  `json:"comparison"` // gte, lte, eq
	Description string  `json:"description"`
}

// Violation records one case where the LLM output disagreed with
// deterministic ground truth and was corrected.
type Violation struct {
	Kind     string `json:"kind"`      // severity | reference | goal | number | parse
	LLMValue string `json:"llm_value"` // what the model returned
	Enforced string `json:"enforced"`  // what the engine used instead
	Detail   string `json:"detail"`
}

// ---------------------------------------------------------------------------
// Interfaces
// ---------------------------------------------------------------------------

// LLMClient is the only required adapter. Wrap OpenAI, Azure, Ollama, etc.
type LLMClient interface {
	Chat(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// RAGStore is an optional vector database integration.
type RAGStore interface {
	Search(ctx context.Context, query string, limit int) ([]RAGResult, error)
}

// RAGResult from vector search.
type RAGResult struct {
	Content   string  `json:"content"`
	Source    string  `json:"source"`
	SourceURL string  `json:"source_url"`
	Score     float64 `json:"score"`
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// Config holds tunable engine parameters. Zero values use sensible defaults.
type Config struct {
	MaxRetries   int      // LLM retry attempts on validation failure (default 2)
	MaxParallel  int      // concurrent category analyses in Analyze (default 4)
	MinTextLen   int      // minimum insight text length (0 = disabled)
	Placeholders []string // substrings that invalidate LLM output
	RAGTopK      int      // max results to retrieve per RAG search (default 3)
	AutoApprove  bool     // skip the human draft/approval gate for DSL proposals (default false)
}

// defaults returns a Config with all zero-value fields replaced by defaults.
func (c Config) defaults() Config {
	if c.MaxRetries <= 0 {
		c.MaxRetries = MaxRetries
	}
	if c.MaxParallel <= 0 {
		c.MaxParallel = MaxParallelLLMCalls
	}
	if c.RAGTopK <= 0 {
		c.RAGTopK = RAGTopK
	}
	if c.MinTextLen <= 0 {
		c.MinTextLen = MinInsightTextLen
	}
	if len(c.Placeholders) == 0 {
		c.Placeholders = InvalidPlaceholders
	}
	return c
}

// ---------------------------------------------------------------------------
// Engine
// ---------------------------------------------------------------------------

// Engine is the main Laozi insight generator.
type Engine struct {
	categories  map[string]Category
	llm         LLMClient
	rag         RAGStore
	context     map[string]interface{}
	mu          sync.RWMutex
	strict      bool
	cfg         Config
	drafts      map[string]*Draft
	nextDraft   int
	reviewer    Reviewer
	audit       AuditSink
	auditStrict bool
}

// New creates a new Laozi engine.
func New(opts ...Option) *Engine {
	e := &Engine{
		categories: make(map[string]Category),
		context:    make(map[string]interface{}),
		drafts:     make(map[string]*Draft),
	}
	for _, opt := range opts {
		opt(e)
	}
	e.cfg = e.cfg.defaults()
	return e
}

// Option configures the engine.
type Option func(*Engine)

// WithLLM sets the LLM client.
func WithLLM(client LLMClient) Option {
	return func(e *Engine) { e.llm = client }
}

// WithRAG enables optional RAG support.
func WithRAG(store RAGStore) Option {
	return func(e *Engine) { e.rag = store }
}

// WithStrict enables strict enforcement: LLM prose containing untraceable
// numbers is replaced with a deterministic template.
func WithStrict(strict bool) Option {
	return func(e *Engine) { e.strict = strict }
}

// WithConfig sets engine configuration (retries, parallelism, validation).
func WithConfig(cfg Config) Option {
	return func(e *Engine) { e.cfg = cfg }
}

// WithContext adds domain context injected into LLM prompts.
func WithContext(key string, value interface{}) Option {
	return func(e *Engine) { e.context[key] = value }
}

// AddCategory registers a category with its thresholds.
// ValidateCategory checks a category is well-formed enough to produce
// trustworthy deterministic output: non-empty ID, and for each threshold a
// metric name, a unit, finite bounds with Min <= Max, consistent optimal range,
// and a source/source URL when references are required.
func ValidateCategory(cat Category) error {
	if strings.TrimSpace(cat.ID) == "" {
		return fmt.Errorf("category ID must be non-empty")
	}
	seen := map[string]bool{}
	for i, t := range cat.Thresholds {
		where := fmt.Sprintf("threshold %d (%q)", i, t.Metric)
		if strings.TrimSpace(t.Metric) == "" {
			return fmt.Errorf("%s: metric name must be non-empty", where)
		}
		if seen[t.Metric] {
			return fmt.Errorf("%s: duplicate metric in category", where)
		}
		seen[t.Metric] = true
		if strings.TrimSpace(t.Unit) == "" {
			return fmt.Errorf("%s: unit must be non-empty", where)
		}
		for name, v := range map[string]float64{"min": t.Min, "max": t.Max, "optimal_min": t.OptimalMin, "optimal_max": t.OptimalMax} {
			if !isFinite(v) {
				return fmt.Errorf("%s: %s must be finite (got %v)", where, name, v)
			}
		}
		if t.Min > t.Max {
			return fmt.Errorf("%s: min (%v) must be <= max (%v)", where, t.Min, t.Max)
		}
		if t.OptimalMin != 0 || t.OptimalMax != 0 {
			if t.OptimalMin > t.OptimalMax {
				return fmt.Errorf("%s: optimal_min (%v) must be <= optimal_max (%v)", where, t.OptimalMin, t.OptimalMax)
			}
		}
		if RequireReference && !cat.Educational {
			if strings.TrimSpace(t.Source) == "" || strings.TrimSpace(t.SourceURL) == "" {
				return fmt.Errorf("%s: source and source URL are required", where)
			}
		}
	}
	return nil
}

// AddCategory validates and registers a category (goroutine-safe). It returns
// an error for malformed input; callers in regulated workflows must check it.
func (e *Engine) AddCategory(cat Category) error {
	if err := ValidateCategory(cat); err != nil {
		return fmt.Errorf("invalid category %q: %w", cat.ID, err)
	}
	e.mu.Lock()
	e.categories[cat.ID] = cat
	e.mu.Unlock()
	return nil
}

// AddCategories validates all categories (and rejects duplicate IDs within the
// batch) before registering any of them.
func (e *Engine) AddCategories(cats []Category) error {
	batch := map[string]bool{}
	for _, cat := range cats {
		if err := ValidateCategory(cat); err != nil {
			return fmt.Errorf("invalid category %q: %w", cat.ID, err)
		}
		if batch[cat.ID] {
			return fmt.Errorf("duplicate category ID in batch: %q", cat.ID)
		}
		batch[cat.ID] = true
	}
	e.mu.Lock()
	for _, cat := range cats {
		e.categories[cat.ID] = cat
	}
	e.mu.Unlock()
	return nil
}

// AddCategories registers multiple categories.
// SetContext updates engine context (goroutine-safe).
func (e *Engine) SetContext(key string, value interface{}) {
	e.mu.Lock()
	e.context[key] = value
	e.mu.Unlock()
}

func (e *Engine) snapshotContext() map[string]interface{} {
	e.mu.RLock()
	defer e.mu.RUnlock()
	cp := make(map[string]interface{}, len(e.context))
	for k, v := range e.context {
		cp[k] = v
	}
	return cp
}

// ---------------------------------------------------------------------------
// Public analysis API
// ---------------------------------------------------------------------------

// Analyze generates insights for all registered categories in parallel,
// bounded by Config.MaxParallel goroutines.
func (e *Engine) Analyze(ctx context.Context, metrics map[string]float64) ([]Insight, error) {
	if e.llm == nil {
		return nil, fmt.Errorf("LLM client not configured")
	}

	type result struct {
		insight *Insight
		err     error
	}

	e.mu.RLock()
	cats := make([]Category, 0, len(e.categories))
	for _, cat := range e.categories {
		cats = append(cats, cat)
	}
	e.mu.RUnlock()
	// Deterministic output order (map iteration order is not stable).
	sort.Slice(cats, func(i, j int) bool { return cats[i].ID < cats[j].ID })

	results := make([]result, len(cats))
	sem := make(chan struct{}, e.cfg.MaxParallel)
	var wg sync.WaitGroup

	for i, cat := range cats {
		wg.Add(1)
		go func(i int, cat Category) {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

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

// AnalyzeCategory generates an insight for a specific category.
func (e *Engine) AnalyzeCategory(ctx context.Context, categoryID string, metrics map[string]float64) (*Insight, error) {
	e.mu.RLock()
	cat, ok := e.categories[categoryID]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("category not found: %s", categoryID)
	}
	return e.analyzeCategory(ctx, cat, metrics)
}

// ---------------------------------------------------------------------------
// Core pipeline: compute → LLM → parse → enforce → validate → retry
// ---------------------------------------------------------------------------

// analyzeCategory runs the per-category pipeline and emits one audit event for
// the produced insight (covering Analyze, AnalyzeCategory, and AnalyzeSelected).
func (e *Engine) analyzeCategory(ctx context.Context, cat Category, metrics map[string]float64) (*Insight, error) {
	requestID := newRequestID()
	ins, err := e.analyzeCategoryInner(ctx, cat, metrics, requestID)
	if err != nil {
		// Record the failed attempt so the audit trail reflects it.
		_ = e.emitAudit(ctx, AuditEvent{
			Time: time.Now(), Kind: "analysis_failed", CategoryID: cat.ID,
			Metrics: metrics, Strict: e.strict, RequestID: requestID,
			Model: LLMModel, CategoryVersion: cat.Version,
			ErrorKind: classifyErr(err), Detail: err.Error(),
		})
		return nil, err
	}
	if ins != nil {
		ev := AuditEvent{
			Time: time.Now(), Kind: "analysis", CategoryID: cat.ID,
			Metrics: metrics, Insight: ins, Strict: e.strict,
			RequestID: requestID, Model: LLMModel, PromptVersion: PromptVersion,
			CategoryVersion: cat.Version,
			Sources:         splitSources(ins.Reference),
			SourceHash:      hashSources(ins.Reference),
		}
		if auditErr := e.emitAudit(ctx, ev); auditErr != nil && e.auditStrict {
			return nil, fmt.Errorf("audit write failed: %w", auditErr)
		}
	}
	return ins, err
}

// newRequestID returns a short random hex correlation ID for one analysis call.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ts-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func splitSources(ref string) []string {
	if strings.TrimSpace(ref) == "" {
		return nil
	}
	return strings.Split(ref, "; ")
}

func hashSources(ref string) string {
	if strings.TrimSpace(ref) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ref))
	return hex.EncodeToString(sum[:])
}

func classifyErr(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "LLM call failed"):
		return "llm"
	case strings.Contains(s, "validation failed"):
		return "validation_retry_exhausted"
	case strings.Contains(s, "parse"):
		return "parse"
	case strings.Contains(s, "audit write failed"):
		return "audit"
	default:
		return "analysis"
	}
}

func (e *Engine) analyzeCategoryInner(ctx context.Context, cat Category, metrics map[string]float64, requestID string) (*Insight, error) {
	// Step 1: Deterministic ground truth.
	comp := computeAnalysis(cat, metrics)

	// Missing required inputs: never call the model and never let it read as a
	// pass. Return the deterministic insufficient-data insight directly.
	if comp.severity == SeverityUnavailable {
		ins := e.buildFallbackInsight(cat, comp)
		detail := []string{}
		if len(comp.missing) > 0 {
			detail = append(detail, "missing: "+strings.Join(comp.missing, ", "))
		}
		if len(comp.invalid) > 0 {
			detail = append(detail, "invalid (non-finite): "+strings.Join(comp.invalid, ", "))
		}
		ins.Violations = append(ins.Violations, Violation{
			Kind:     "unavailable",
			Enforced: string(SeverityUnavailable),
			Detail:   strings.Join(detail, "; "),
		})
		return ins, nil
	}

	guidelinesText := e.buildGuidelinesText(cat)

	var ragContext string
	var ragResults []RAGResult
	if e.rag != nil && cat.RAGQuery != "" {
		var err error
		ragResults, err = e.rag.Search(ctx, cat.RAGQuery, e.cfg.RAGTopK)
		if err != nil || len(ragResults) == 0 {
			if cat.RequireRAG {
				// Fail closed: required evidence is unavailable, so make no
				// determination and record the retrieval failure.
				detail := "required retrieval returned no results"
				if err != nil {
					detail = "required retrieval failed: " + err.Error()
				}
				_ = e.emitAudit(ctx, AuditEvent{
					Time: time.Now(), Kind: "retrieval_failed", CategoryID: cat.ID,
					RequestID: requestID, Model: LLMModel, CategoryVersion: cat.Version,
					ErrorKind: "retrieval", Detail: detail,
				})
				ins := e.buildFallbackInsight(cat, comp)
				ins.Severity = SeverityUnavailable
				ins.Text = "Insufficient evidence: required reference retrieval was unavailable, so no determination could be made."
				ins.Violations = append(ins.Violations, Violation{
					Kind: "rag_unavailable", Enforced: string(SeverityUnavailable), Detail: detail,
				})
				return ins, nil
			}
			// Optional RAG: proceed without retrieved context.
		} else {
			ragContext = e.buildRAGContext(ragResults)
		}
	}

	systemPrompt := e.buildSystemPrompt()
	userPrompt := e.buildUserPrompt(cat, guidelinesText, comp.metricsText, ragContext, comp.severity)

	// Step 2+: LLM → parse → enforce → validate, with retries.
	var lastErr error
	for attempt := 0; attempt <= e.cfg.MaxRetries; attempt++ {
		prompt := userPrompt
		if attempt > 0 && lastErr != nil {
			// On retry, append the regeneration template carrying the error.
			prompt = userPrompt + renderTemplate(RegenerationPromptTemplate, regenPromptData{Error: lastErr.Error()})
		}

		response, err := e.llm.Chat(ctx, systemPrompt, prompt)
		if err != nil {
			if e.strict {
				// LLM failure in strict mode → deterministic fallback.
				return e.buildFallbackInsight(cat, comp), nil
			}
			return nil, fmt.Errorf("LLM call failed: %w", err)
		}

		insight, parseErr := e.parseResponse(response, cat)
		if parseErr != nil {
			if e.strict {
				ins := e.buildFallbackInsight(cat, comp)
				ins.Violations = append(ins.Violations, Violation{
					Kind: "parse", LLMValue: truncate(response, 200),
					Enforced: "deterministic template used",
					Detail:   fmt.Sprintf("LLM response could not be parsed: %v", parseErr),
				})
				return ins, nil
			}
			lastErr = parseErr
			continue // retry
		}

		// Enforce — reconcile against ground truth.
		e.enforce(insight, cat, comp, ragResults)

		// Validate — structural checks.
		if valErr := e.validate(insight); valErr != nil {
			lastErr = valErr
			continue // retry
		}

		return insight, nil
	}

	// All retries exhausted.
	if e.strict {
		return e.buildFallbackInsight(cat, comp), nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("validation failed after %d attempts: %w", e.cfg.MaxRetries+1, lastErr)
	}
	return nil, fmt.Errorf("analysis failed for category %s", cat.ID)
}

// validate checks structural properties of the insight text.
func (e *Engine) validate(insight *Insight) error {
	// Placeholders first: a "[PLACEHOLDER]" string is a more specific failure
	// than "too short", and tests/users expect the placeholder error to surface.
	for _, ph := range e.cfg.Placeholders {
		if strings.Contains(insight.Text, ph) {
			return fmt.Errorf("contains placeholder: %s", ph)
		}
	}
	if RequireReference && strings.TrimSpace(insight.Reference) == "" {
		return fmt.Errorf("missing required reference")
	}
	if e.cfg.MinTextLen > 0 && len(insight.Text) < e.cfg.MinTextLen {
		return fmt.Errorf("text too short: %d < %d", len(insight.Text), e.cfg.MinTextLen)
	}
	return nil
}

// buildFallbackInsight creates a fully deterministic insight.
func (e *Engine) buildFallbackInsight(cat Category, c computedAnalysis) *Insight {
	var relatedMetrics []string
	for _, t := range cat.Thresholds {
		relatedMetrics = append(relatedMetrics, t.Metric)
	}
	return &Insight{
		ID:             fmt.Sprintf("%s-001", cat.ID),
		CategoryID:     cat.ID,
		CategoryName:   cat.Name,
		Text:           c.renderText(),
		Severity:       c.severity,
		RelatedMetrics: relatedMetrics,
		Reference:      c.reference,
	}
}

// ---------------------------------------------------------------------------
// Deterministic ground truth computation
// ---------------------------------------------------------------------------

type computedAnalysis struct {
	severity    Severity
	metricsText string
	reference   string
	lines       []metricLine
	allowed     []float64
	missing     []string // required metrics that were absent from the input
	invalid     []string // metrics supplied as non-finite (NaN/±Inf)
}

type metricLine struct {
	metric string
	value  float64
	unit   string
	min    float64
	max    float64
	status string // "BELOW minimum" | "WITHIN range" | "ABOVE maximum"
}

func computeAnalysis(cat Category, metrics map[string]float64) computedAnalysis {
	c := computedAnalysis{severity: SeveritySuccess}
	var sb strings.Builder
	seenRef := map[string]bool{}
	var refs []string

	evaluated := 0
	missingRequired := 0
	for _, t := range cat.Thresholds {
		if t.SourceURL != "" {
			key := t.Source + "|" + t.SourceURL
			if !seenRef[key] {
				seenRef[key] = true
				refs = append(refs, fmt.Sprintf("%s - %s", t.Source, t.SourceURL))
			}
		}

		// A non-finite bound makes the threshold uncomputable. Registration
		// validation should catch this, but never silently evaluate against it.
		if !isFinite(t.Min) || !isFinite(t.Max) {
			c.invalid = append(c.invalid, t.Metric)
			if !t.Optional {
				missingRequired++
			}
			continue
		}

		val, ok := metrics[t.Metric]
		if !ok {
			c.missing = append(c.missing, t.Metric)
			if !t.Optional {
				missingRequired++
			}
			continue
		}
		// Reject non-finite inputs: NaN < Min and NaN > Max are both false, so a
		// NaN would otherwise fall through to "WITHIN range" and read as success.
		if !isFinite(val) {
			c.invalid = append(c.invalid, t.Metric)
			if !t.Optional {
				missingRequired++
			}
			continue
		}
		evaluated++

		var status string
		switch {
		case val < t.Min:
			status = "BELOW minimum"
			c.severity = SeverityWarning
		case val > t.Max:
			status = "ABOVE maximum"
			c.severity = SeverityWarning
		default:
			status = "WITHIN range"
		}

		sb.WriteString(fmt.Sprintf("- %s: %.2f %s → %s (%.2f - %.2f)\n",
			t.Metric, val, t.Unit, status, t.Min, t.Max))

		c.lines = append(c.lines, metricLine{
			metric: t.Metric, value: val, unit: t.Unit,
			min: t.Min, max: t.Max, status: status,
		})
		c.allowed = append(c.allowed, val, t.Min, t.Max)
		if t.OptimalMin > 0 {
			c.allowed = append(c.allowed, t.OptimalMin)
		}
		if t.OptimalMax > 0 {
			c.allowed = append(c.allowed, t.OptimalMax)
		}
	}

	if cat.Educational {
		c.severity = SeverityInfo
	} else if len(cat.Thresholds) > 0 && (missingRequired > 0 || evaluated == 0) {
		// Any required metric missing or non-finite (or nothing evaluable at all)
		// means no determination can be made. A missing/invalid input must never
		// read as "success".
		c.severity = SeverityUnavailable
	}
	c.metricsText = sb.String()
	c.reference = strings.Join(refs, "; ")
	return c
}

// isFinite reports whether f is a usable real number (not NaN or ±Inf).
func isFinite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// --- deep-copy helpers: getters return copies so callers cannot mutate
// internal engine/audit state (drafts, categories, domains, audit entries). ---

func cloneStrings(s []string) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneFloatMap(m map[string]float64) map[string]float64 {
	if m == nil {
		return nil
	}
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// cloneThresholds is a full copy: Threshold has only scalar fields.
func cloneThresholds(t []Threshold) []Threshold {
	if t == nil {
		return nil
	}
	out := make([]Threshold, len(t))
	copy(out, t)
	return out
}

func cloneCategory(c Category) Category {
	c.Thresholds = cloneThresholds(c.Thresholds)
	return c
}

func cloneCategoryPtr(c *Category) *Category {
	if c == nil {
		return nil
	}
	cc := cloneCategory(*c)
	return &cc
}

func cloneInsight(i *Insight) *Insight {
	if i == nil {
		return nil
	}
	ci := *i
	ci.RelatedMetrics = cloneStrings(i.RelatedMetrics)
	ci.Metadata = cloneStringMap(i.Metadata)
	if i.Violations != nil {
		v := make([]Violation, len(i.Violations))
		copy(v, i.Violations)
		ci.Violations = v
	}
	if i.SuggestedGoal != nil {
		g := *i.SuggestedGoal
		ci.SuggestedGoal = &g
	}
	return &ci
}

// ---------------------------------------------------------------------------
// Post-LLM enforcement
// ---------------------------------------------------------------------------

func (e *Engine) enforce(insight *Insight, cat Category, c computedAnalysis, ragResults []RAGResult) {
	// 1. Severity — deterministic always wins.
	if insight.Severity != c.severity {
		insight.Violations = append(insight.Violations, Violation{
			Kind: "severity", LLMValue: string(insight.Severity),
			Enforced: string(c.severity),
			Detail:   "severity overridden by deterministic comparison",
		})
		insight.Severity = c.severity
	}

	// 2. Reference — built from threshold metadata plus RAG sources.
	//    Append RAG source URLs to the canonical reference so the LLM
	//    can legitimately cite them.
	fullRef := c.reference
	if len(ragResults) > 0 {
		for _, r := range ragResults {
			if r.SourceURL != "" {
				entry := r.Source + " - " + r.SourceURL
				if !strings.Contains(fullRef, r.SourceURL) {
					if fullRef != "" {
						fullRef += "; "
					}
					fullRef += entry
				}
			}
		}
	}

	if fullRef != "" {
		if !referenceMatches(insight.Reference, cat, ragResults) {
			insight.Violations = append(insight.Violations, Violation{
				Kind: "reference", LLMValue: insight.Reference,
				Enforced: fullRef,
				Detail:   "citation did not match any provided source URL; replaced",
			})
		}
		insight.Reference = fullRef
	}

	// 3. Suggested goal — must come from a real threshold.
	if insight.SuggestedGoal != nil {
		if v := enforceGoal(insight.SuggestedGoal, cat, c); v != nil {
			insight.Violations = append(insight.Violations, *v)
		}
	}

	// 4. Numbers in prose — heuristic trace check.
	if bad := c.unknownNumbers(insight.Text); len(bad) > 0 {
		v := Violation{
			Kind: "number", LLMValue: strings.Join(bad, ", "),
			Detail: "narrative contains numbers not traceable to data or thresholds",
		}
		if e.strict {
			insight.Text = c.renderText()
			v.Enforced = "narration replaced with deterministic template"
		}
		insight.Violations = append(insight.Violations, v)
	}
}

func referenceMatches(ref string, cat Category, ragResults []RAGResult) bool {
	for _, t := range cat.Thresholds {
		if t.SourceURL != "" && strings.Contains(ref, t.SourceURL) {
			return true
		}
	}
	for _, r := range ragResults {
		if r.SourceURL != "" && strings.Contains(ref, r.SourceURL) {
			return true
		}
	}
	return false
}

func enforceGoal(g *SuggestedGoal, cat Category, c computedAnalysis) *Violation {
	var th *Threshold
	for i := range cat.Thresholds {
		if cat.Thresholds[i].Metric == g.Metric {
			th = &cat.Thresholds[i]
			break
		}
	}
	if th == nil {
		orig := fmt.Sprintf("%+v", *g)
		*g = SuggestedGoal{}
		return &Violation{Kind: "goal", LLMValue: orig,
			Detail: "suggested goal referenced an unknown metric; dropped"}
	}

	target, cmp := th.Min, "gte"
	for _, line := range c.lines {
		if line.metric == g.Metric && line.status == "ABOVE maximum" {
			target, cmp = th.Max, "lte"
		}
	}

	if !sameAtDisplay(g.Target, target) || g.Unit != th.Unit || g.Comparison != cmp {
		orig := fmt.Sprintf("target=%.2f unit=%s cmp=%s", g.Target, g.Unit, g.Comparison)
		g.Target, g.Unit, g.Comparison = target, th.Unit, cmp
		return &Violation{Kind: "goal", LLMValue: orig,
			Enforced: fmt.Sprintf("target=%.2f unit=%s cmp=%s", target, th.Unit, cmp),
			Detail:   "goal target/unit/comparison aligned to threshold bound"}
	}
	return nil
}

func (c computedAnalysis) renderText() string {
	if len(c.lines) == 0 {
		parts := []string{}
		if len(c.missing) > 0 {
			parts = append(parts, "missing: "+strings.Join(c.missing, ", "))
		}
		if len(c.invalid) > 0 {
			parts = append(parts, "invalid (non-finite): "+strings.Join(c.invalid, ", "))
		}
		if len(parts) > 0 {
			return "Insufficient data: required metric(s) could not be evaluated (" +
				strings.Join(parts, "; ") + "), so no determination could be made."
		}
		return "Insufficient data: no metrics were available, so no determination could be made."
	}
	var sb strings.Builder
	for _, l := range c.lines {
		switch l.status {
		case "BELOW minimum":
			sb.WriteString(fmt.Sprintf(
				"%s is %.2f %s, below the recommended minimum of %.2f. Aim to raise it to at least %.2f. ",
				l.metric, l.value, l.unit, l.min, l.min))
		case "ABOVE maximum":
			sb.WriteString(fmt.Sprintf(
				"%s is %.2f %s, above the recommended maximum of %.2f. Aim to bring it under %.2f. ",
				l.metric, l.value, l.unit, l.max, l.max))
		default:
			sb.WriteString(fmt.Sprintf(
				"%s is %.2f %s, within the healthy range of %.2f to %.2f. ",
				l.metric, l.value, l.unit, l.min, l.max))
		}
	}
	return strings.TrimSpace(sb.String())
}

// ---------------------------------------------------------------------------
// Number tracing (heuristic)
// ---------------------------------------------------------------------------

var numRe = regexp.MustCompile(`-?[\d,]+(?:\.\d+)?`)

// unknownNumbers returns numeric tokens not traceable to data or thresholds.
//
// HONEST CAVEAT: this is heuristic. Free prose legitimately contains numbers
// that aren't metrics ("over the last 30 days"), so it can produce false
// positives. Severity, reference, and goal are the hard guarantees. For prose,
// the real lever is strict mode.
func (c computedAnalysis) unknownNumbers(text string) []string {
	var bad []string
	for _, tok := range numRe.FindAllString(text, -1) {
		n, err := parseNum(tok)
		if err != nil {
			continue
		}
		if !c.isAllowed(n) {
			bad = append(bad, tok)
		}
	}
	return bad
}

func (c computedAnalysis) isAllowed(n float64) bool {
	for _, a := range c.allowed {
		if sameAtDisplay(a, n) {
			return true
		}
	}
	return false
}

func parseNum(tok string) (float64, error) {
	return strconv.ParseFloat(strings.ReplaceAll(tok, ",", ""), 64)
}

// displayDecimals is the precision at which metric values are presented to the
// model (the "%.2f" metric/guideline formatting). Traceability is checked at
// exactly this precision: a number in the narrative is allowed only if it
// renders identically to a computed or threshold value. This is exact matching
// at display precision — there is no tolerance window in which a fabricated
// "close" number could pass as traceable (C-04). Keep this in sync with the
// "%.2f" formatting used to build prompts.
const displayDecimals = 2

func sameAtDisplay(a, b float64) bool {
	return strconv.FormatFloat(a, 'f', displayDecimals, 64) ==
		strconv.FormatFloat(b, 'f', displayDecimals, 64)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ---------------------------------------------------------------------------
// Prompt construction
// ---------------------------------------------------------------------------

func (e *Engine) buildGuidelinesText(cat Category) string {
	var sb strings.Builder
	for _, t := range cat.Thresholds {
		sb.WriteString(fmt.Sprintf("📊 %s GUIDELINE:\n", strings.ToUpper(t.Metric)))
		sb.WriteString(fmt.Sprintf("   Range: %.2f - %.2f %s\n", t.Min, t.Max, t.Unit))
		if t.OptimalMin > 0 || t.OptimalMax > 0 {
			sb.WriteString(fmt.Sprintf("   Optimal: %.2f - %.2f %s\n", t.OptimalMin, t.OptimalMax, t.Unit))
		}
		sb.WriteString(fmt.Sprintf("   Source: %s\n", t.Source))
		sb.WriteString(fmt.Sprintf("   URL: %s\n", t.SourceURL))
		if t.Description != "" {
			sb.WriteString(fmt.Sprintf("   Note: %s\n", t.Description))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func (e *Engine) buildRAGContext(results []RAGResult) string {
	var sb strings.Builder
	sb.WriteString("ADDITIONAL REFERENCES:\n")
	for i, r := range results {
		sb.WriteString(fmt.Sprintf("%d. %s\n   Source: %s - %s\n\n", i+1, r.Content, r.Source, r.SourceURL))
	}
	return sb.String()
}

func (e *Engine) buildSystemPrompt() string {
	return SystemPromptTemplate
}

func (e *Engine) buildUserPrompt(cat Category, guidelines, comparison, ragContext string, severity Severity) string {
	contextJSON, _ := json.Marshal(e.snapshotContext())

	if cat.Educational {
		return renderTemplate(EducationalPromptTemplate, eduPromptData{
			CategoryID:   cat.ID,
			CategoryName: cat.Name,
			Context:      string(contextJSON),
			RAGContext:   ragContext,
		})
	}

	metricName := ""
	if len(cat.Thresholds) > 0 {
		metricName = cat.Thresholds[0].Metric
	}

	return renderTemplate(MetricPromptTemplate, metricPromptData{
		CategoryID:       cat.ID,
		CategoryName:     cat.Name,
		Guidelines:       guidelines,
		RAGContext:       ragContext,
		Context:          string(contextJSON),
		Comparison:       comparison,
		ExpectedSeverity: string(severity),
		MetricName:       metricName,
	})
}

// ---------------------------------------------------------------------------
// Response parsing
// ---------------------------------------------------------------------------

func (e *Engine) parseResponse(response string, cat Category) (*Insight, error) {
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	var result struct {
		Insight struct {
			Text          string         `json:"text"`
			Severity      Severity       `json:"severity"`
			Reference     string         `json:"reference"`
			SuggestedGoal *SuggestedGoal `json:"suggested_goal"`
		} `json:"insight"`
	}

	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}

	if !isValidSeverity(result.Insight.Severity) {
		return nil, fmt.Errorf("invalid severity %q (must be one of warning/success/info)", result.Insight.Severity)
	}

	var relatedMetrics []string
	for _, t := range cat.Thresholds {
		relatedMetrics = append(relatedMetrics, t.Metric)
	}

	return &Insight{
		ID:             fmt.Sprintf("%s-001", cat.ID),
		CategoryID:     cat.ID,
		CategoryName:   cat.Name,
		Text:           result.Insight.Text,
		Severity:       result.Insight.Severity,
		RelatedMetrics: relatedMetrics,
		Reference:      result.Insight.Reference,
		SuggestedGoal:  result.Insight.SuggestedGoal,
	}, nil
}

// ---------------------------------------------------------------------------
// Template rendering (templates are the source of truth in config.go)
// ---------------------------------------------------------------------------

type metricPromptData struct {
	CategoryID       string
	CategoryName     string
	Guidelines       string
	RAGContext       string
	Context          string
	Comparison       string
	ExpectedSeverity string
	MetricName       string
}

type eduPromptData struct {
	CategoryID   string
	CategoryName string
	Context      string
	RAGContext   string
}

type regenPromptData struct{ Error string }

// renderTemplate executes a config.go prompt template against data. The
// templates are package constants, so a parse/exec failure is a programming
// error; we degrade to the raw template rather than panicking in production.
func renderTemplate(tmpl string, data interface{}) string {
	t, err := template.New("prompt").Parse(tmpl)
	if err != nil {
		return tmpl
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return tmpl
	}
	return b.String()
}

func isValidSeverity(s Severity) bool {
	for _, v := range ValidSeverities {
		if s == v {
			return true
		}
	}
	return false
}
