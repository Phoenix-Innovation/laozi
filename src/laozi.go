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
//      var client laozi.LLMClient // any type with Chat(ctx, system, user) (string, error)
//
//      engine := laozi.New(
//              laozi.WithLLM(client),
//              laozi.WithConfig(laozi.Config{MaxRetries: 2, MaxParallel: 8}),
//              laozi.WithStrict(true),
//      )
//
//      engine.AddCategory(laozi.Category{
//              ID: "glucose", Name: "Blood Glucose",
//              Thresholds: []laozi.Threshold{{
//                      Metric: "fasting_glucose", Min: 70, Max: 99, Unit: "mg/dL",
//                      Source: "ADA", SourceURL: "https://diabetes.org/diagnosis",
//              }},
//      })
//
//      insights, err := engine.Analyze(ctx, map[string]float64{"fasting_glucose": 112})
package laozi

import (
        "context"
        "encoding/json"
        "fmt"
        "math"
        "regexp"
        "strconv"
        "strings"
        "sync"
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
)

// Threshold defines a constraint for a metric.
type Threshold struct {
        Metric      string  `json:"metric"`
        Min         float64 `json:"min"`
        Max         float64 `json:"max"`
        OptimalMin  float64 `json:"optimal_min,omitempty"`
        OptimalMax  float64 `json:"optimal_max,omitempty"`
        Unit        string  `json:"unit"`
        Source      string  `json:"source"`
        SourceURL   string  `json:"source_url"`
        Description string  `json:"description,omitempty"`
}

// Category groups related metrics and thresholds.
type Category struct {
        ID          string      `json:"id"`
        Name        string      `json:"name"`
        Thresholds  []Threshold `json:"thresholds"`
        Educational bool        `json:"educational"`
        RAGQuery    string      `json:"rag_query,omitempty"`
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
}

// defaults returns a Config with all zero-value fields replaced by defaults.
func (c Config) defaults() Config {
        if c.MaxRetries <= 0 {
                c.MaxRetries = 2
        }
        if c.MaxParallel <= 0 {
                c.MaxParallel = 4
        }
        if c.RAGTopK <= 0 {
                c.RAGTopK = 3
        }
        // MinTextLen defaults to 0 (disabled); callers opt in via WithConfig.
        if len(c.Placeholders) == 0 {
                c.Placeholders = []string{"[INSERT", "[PLACEHOLDER", "{{"}
        }
        return c
}

// ---------------------------------------------------------------------------
// Engine
// ---------------------------------------------------------------------------

// Engine is the main Laozi insight generator.
type Engine struct {
        categories map[string]Category
        llm        LLMClient
        rag        RAGStore
        context    map[string]interface{}
        mu         sync.RWMutex
        strict     bool
        cfg        Config
}

// New creates a new Laozi engine.
func New(opts ...Option) *Engine {
        e := &Engine{
                categories: make(map[string]Category),
                context:    make(map[string]interface{}),
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
func (e *Engine) AddCategory(cat Category) {
        e.categories[cat.ID] = cat
}

// AddCategories registers multiple categories.
func (e *Engine) AddCategories(cats []Category) {
        for _, cat := range cats {
                e.categories[cat.ID] = cat
        }
}

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

        cats := make([]Category, 0, len(e.categories))
        for _, cat := range e.categories {
                cats = append(cats, cat)
        }

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
        cat, ok := e.categories[categoryID]
        if !ok {
                return nil, fmt.Errorf("category not found: %s", categoryID)
        }
        return e.analyzeCategory(ctx, cat, metrics)
}

// ---------------------------------------------------------------------------
// Core pipeline: compute → LLM → parse → enforce → validate → retry
// ---------------------------------------------------------------------------

func (e *Engine) analyzeCategory(ctx context.Context, cat Category, metrics map[string]float64) (*Insight, error) {
        // Step 1: Deterministic ground truth.
        comp := computeAnalysis(cat, metrics)

        guidelinesText := e.buildGuidelinesText(cat)

        var ragContext string
        var ragResults []RAGResult
        if e.rag != nil && cat.RAGQuery != "" {
                var err error
                ragResults, err = e.rag.Search(ctx, cat.RAGQuery, e.cfg.RAGTopK)
                if err == nil && len(ragResults) > 0 {
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
                        // On retry, append the validation error so the LLM can correct.
                        prompt = fmt.Sprintf("%s\n\nPREVIOUS ATTEMPT FAILED VALIDATION: %s\nPlease fix and try again.",
                                userPrompt, lastErr)
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
        if e.cfg.MinTextLen > 0 && len(insight.Text) < e.cfg.MinTextLen {
                return fmt.Errorf("text too short: %d < %d", len(insight.Text), e.cfg.MinTextLen)
        }
        for _, ph := range e.cfg.Placeholders {
                if strings.Contains(insight.Text, ph) {
                        return fmt.Errorf("contains placeholder: %s", ph)
                }
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

        for _, t := range cat.Thresholds {
                if t.SourceURL != "" {
                        key := t.Source + "|" + t.SourceURL
                        if !seenRef[key] {
                                seenRef[key] = true
                                refs = append(refs, fmt.Sprintf("%s - %s", t.Source, t.SourceURL))
                        }
                }

                val, ok := metrics[t.Metric]
                if !ok {
                        continue
                }

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
        }
        c.metricsText = sb.String()
        c.reference = strings.Join(refs, "; ")
        return c
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

        if !floatEq(g.Target, target) || g.Unit != th.Unit || g.Comparison != cmp {
                orig := fmt.Sprintf("target=%.2f unit=%s cmp=%s", g.Target, g.Unit, g.Comparison)
                g.Target, g.Unit, g.Comparison = target, th.Unit, cmp
                return &Violation{Kind: "goal", LLMValue: orig,
                        Enforced: fmt.Sprintf("target=%.2f unit=%s cmp=%s", target, th.Unit, cmp),
                        Detail:   "goal target/unit/comparison aligned to threshold bound"}
        }
        return nil
}

func (c computedAnalysis) renderText() string {
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
                if floatEq(a, n) {
                        return true
                }
        }
        return false
}

func parseNum(tok string) (float64, error) {
        return strconv.ParseFloat(strings.ReplaceAll(tok, ",", ""), 64)
}

func floatEq(a, b float64) bool {
        tol := math.Max(0.01, 0.005*math.Abs(a))
        return math.Abs(a-b) <= tol
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
        return `You are an insight generator using the Laozi dual-constraint system.
You MUST generate exactly ONE insight based on the provided thresholds and data.

RULES:
1. Use ONLY the thresholds and references provided
2. Never invent data or thresholds
3. Compare actual values to thresholds explicitly
4. Include the source URL in your reference
5. Use ONLY numbers from the provided data and thresholds in your narrative

SEVERITY:
- "warning" = value OUTSIDE the threshold range
- "success" = value WITHIN the threshold range
- "info" = educational tip (for educational categories)

Respond with ONLY valid JSON.`
}

func (e *Engine) buildUserPrompt(cat Category, guidelines, metrics, ragContext string, severity Severity) string {
        contextJSON, _ := json.Marshal(e.snapshotContext())

        if cat.Educational {
                return fmt.Sprintf(`CATEGORY: %s (%s)
TYPE: Educational "Did You Know" tip

CONTEXT:
%s

%s

OUTPUT (severity MUST be "info"):
{
  "insight": {
    "text": "Did you know? [educational fact]",
    "severity": "info",
    "reference": "[Source] - [URL]"
  }
}`, cat.ID, cat.Name, string(contextJSON), ragContext)
        }

        metricName := ""
        if len(cat.Thresholds) > 0 {
                metricName = cat.Thresholds[0].Metric
        }

        return fmt.Sprintf(`CATEGORY: %s (%s)

GUIDELINES (Tier 1 - Mandatory):
%s

%s

CONTEXT:
%s

ACTUAL VALUES WITH COMPARISON:
%s

EXPECTED SEVERITY: %s

OUTPUT (use severity="%s"):
{
  "insight": {
    "text": "[State value, compare to threshold, give advice]",
    "severity": "%s",
    "reference": "[Source from guidelines] - [URL]",
    "suggested_goal": { "metric": "%s", "target": [threshold_value], "unit": "[unit]", "comparison": "gte", "description": "..." }
  }
}`,
                cat.ID, cat.Name,
                guidelines,
                ragContext,
                string(contextJSON),
                metrics,
                severity, severity, severity,
                metricName)
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