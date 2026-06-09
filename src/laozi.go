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
//      engine := laozi.New(
//              laozi.WithLLM("https://api.openai.com/v1", "your-api-key", "gpt-4o"),
//              laozi.WithRAG(myRAGStore), // optional
//              laozi.WithStrict(true),     // optional: replace LLM prose on number violations
//      )
//
//      // Define your domain thresholds
//      engine.AddCategory(laozi.Category{
//              ID:   "revenue",
//              Name: "Revenue Analysis",
//              Thresholds: []laozi.Threshold{
//                      {Metric: "monthly_revenue", Min: 100000, Max: 500000, Unit: "USD"},
//              },
//      })
//
//      // Generate insights — every insight carries a Violations audit trail
//      insights, err := engine.Analyze(ctx, map[string]float64{
//              "monthly_revenue": 75000,
//      })
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

// Severity levels for insights
type Severity string

const (
        SeverityWarning Severity = "warning" // Value outside threshold
        SeveritySuccess Severity = "success" // Value within threshold
        SeverityInfo    Severity = "info"    // Educational/informational
)

// Threshold defines a constraint for a metric
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

// Category groups related metrics and thresholds
type Category struct {
        ID          string      `json:"id"`
        Name        string      `json:"name"`
        Thresholds  []Threshold `json:"thresholds"`
        Educational bool        `json:"educational"` // If true, generates "Did You Know" tips
        RAGQuery    string      `json:"rag_query,omitempty"`
}

// Insight is the output from analysis. The Violations slice is the audit trail:
// it records every case where the LLM deviated from deterministic truth and
// was corrected. An empty Violations slice means the LLM was fully compliant.
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

// SuggestedGoal provides actionable targets
type SuggestedGoal struct {
        Metric      string  `json:"metric"`
        Target      float64 `json:"target"`
        Unit        string  `json:"unit"`
        Comparison  string  `json:"comparison"` // gte, lte, eq
        Description string  `json:"description"`
}

// Violation records one case where the LLM output disagreed with the
// deterministic ground truth and was corrected. Attaching these to every
// Insight is what makes the audit trail real: a caller (or auditor) can see
// exactly what the model tried to do and what the engine enforced instead.
type Violation struct {
        Kind     string `json:"kind"`      // severity | reference | goal | number | parse
        LLMValue string `json:"llm_value"` // what the model returned
        Enforced string `json:"enforced"`  // what the engine used instead (empty = flagged only)
        Detail   string `json:"detail"`
}

// ---------------------------------------------------------------------------
// Interfaces
// ---------------------------------------------------------------------------

// RAGStore interface for optional vector database integration
type RAGStore interface {
        Search(ctx context.Context, query string, limit int) ([]RAGResult, error)
}

// RAGResult from vector search
type RAGResult struct {
        Content   string  `json:"content"`
        Source    string  `json:"source"`
        SourceURL string  `json:"source_url"`
        Score     float64 `json:"score"`
}

// LLMClient interface for LLM integration
type LLMClient interface {
        Chat(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// ---------------------------------------------------------------------------
// Engine
// ---------------------------------------------------------------------------

// Engine is the main Laozi insight generator. The strict field controls
// whether LLM prose containing untraceable numbers is replaced with a
// deterministic template (true) or merely flagged (false, default).
type Engine struct {
        categories map[string]Category
        llm        LLMClient
        rag        RAGStore // optional
        context    map[string]interface{}
        mu         sync.RWMutex // protects context map for concurrent access
        strict     bool
}

// New creates a new Laozi engine
func New(opts ...Option) *Engine {
        e := &Engine{
                categories: make(map[string]Category),
                context:    make(map[string]interface{}),
        }
        for _, opt := range opts {
                opt(e)
        }
        return e
}

// Option configures the engine
type Option func(*Engine)

// WithLLM sets the LLM client
func WithLLM(client LLMClient) Option {
        return func(e *Engine) { e.llm = client }
}

// WithRAG enables optional RAG support
func WithRAG(store RAGStore) Option {
        return func(e *Engine) { e.rag = store }
}

// WithStrict controls what happens when the LLM's narrative prose contains
// numbers that can't be traced to the input data or thresholds.
//
//   - false (default): such cases are recorded as Violations but the LLM text
//     is kept.
//   - true: the narration is discarded and replaced with a deterministic,
//     number-correct template. In strict mode the LLM is cut out entirely the
//     moment it steps out of line — this is what makes "the LLM has limited
//     authority" a property of the system rather than a hope about the prompt.
func WithStrict(strict bool) Option {
        return func(e *Engine) { e.strict = strict }
}

// WithContext adds domain context (e.g., user profile, entity info)
func WithContext(key string, value interface{}) Option {
        return func(e *Engine) { e.context[key] = value }
}

// AddCategory registers a category with its thresholds
func (e *Engine) AddCategory(cat Category) {
        e.categories[cat.ID] = cat
}

// AddCategories registers multiple categories
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

// snapshotContext returns a shallow copy of the context map for safe
// concurrent reading (e.g., JSON marshalling in prompt construction).
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

// Analyze generates insights for the given metrics across all categories.
func (e *Engine) Analyze(ctx context.Context, metrics map[string]float64) ([]Insight, error) {
        if e.llm == nil {
                return nil, fmt.Errorf("LLM client not configured")
        }

        var insights []Insight

        for _, cat := range e.categories {
                insight, err := e.analyzeCategory(ctx, cat, metrics)
                if err != nil {
                        return nil, fmt.Errorf("category %s: %w", cat.ID, err)
                }
                if insight != nil {
                        insights = append(insights, *insight)
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
// Core pipeline: compute → LLM → enforce
// ---------------------------------------------------------------------------

func (e *Engine) analyzeCategory(ctx context.Context, cat Category, metrics map[string]float64) (*Insight, error) {
        // Step 1: Deterministic ground truth — computed entirely in Go.
        comp := computeAnalysis(cat, metrics)

        // Build threshold guidelines text (for the LLM prompt)
        guidelinesText := e.buildGuidelinesText(cat)

        // Get RAG context if available
        var ragContext string
        if e.rag != nil && cat.RAGQuery != "" {
                results, err := e.rag.Search(ctx, cat.RAGQuery, 2)
                if err == nil && len(results) > 0 {
                        ragContext = e.buildRAGContext(results)
                }
        }

        // Build prompts — the prompt includes the EXPECTED SEVERITY so a
        // compliant LLM can echo it, but enforcement doesn't depend on that.
        systemPrompt := e.buildSystemPrompt()
        userPrompt := e.buildUserPrompt(cat, guidelinesText, comp.metricsText, ragContext, comp.severity)

        // Step 2: Call LLM
        response, err := e.llm.Chat(ctx, systemPrompt, userPrompt)
        if err != nil {
                return nil, fmt.Errorf("LLM call failed: %w", err)
        }

        // Step 3: Parse response
        insight, parseErr := e.parseResponse(response, cat)

        if parseErr != nil {
                // In strict mode, parse failure is recoverable: fall back to
                // deterministic template. In lax mode, it's a hard error.
                if e.strict {
                        insight = e.buildFallbackInsight(cat, comp)
                        insight.Violations = append(insight.Violations, Violation{
                                Kind:     "parse",
                                LLMValue: truncate(response, 200),
                                Enforced: "deterministic template used",
                                Detail:   fmt.Sprintf("LLM response could not be parsed: %v", parseErr),
                        })
                        return insight, nil
                }
                return nil, parseErr
        }

        // Step 4: Enforce — reconcile LLM output against deterministic truth.
        // After this call, severity, reference, and goal are GUARANTEED correct.
        e.enforce(insight, cat, comp)

        return insight, nil
}

// buildFallbackInsight creates a fully deterministic insight when the LLM
// output cannot be parsed (strict mode only).
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

// computedAnalysis is the deterministic ground truth for one category + metric
// set. Everything here is computed in Go, before and independently of the LLM.
// On any conflict, these values win.
type computedAnalysis struct {
        severity    Severity
        metricsText string       // human-readable comparison block for the prompt
        reference   string       // canonical citation built from threshold metadata
        lines       []metricLine // per-metric facts, for the template fallback
        allowed     []float64    // numbers the LLM is permitted to state in prose
}

type metricLine struct {
        metric string
        value  float64
        unit   string
        min    float64
        max    float64
        status string // "BELOW minimum" | "WITHIN range" | "ABOVE maximum"
}

// computeAnalysis is the deterministic core. It produces prompt text and
// captures the structured ground truth used for enforcement.
func computeAnalysis(cat Category, metrics map[string]float64) computedAnalysis {
        c := computedAnalysis{severity: SeveritySuccess}
        var sb strings.Builder
        seenRef := map[string]bool{}
        var refs []string

        for _, t := range cat.Thresholds {
                // Collect canonical references regardless of whether the metric is present.
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

// enforce reconciles an LLM-produced Insight against the deterministic ground
// truth, mutating it in place and recording every correction. After this runs,
// severity, reference, and the suggested goal are GUARANTEED to reflect the
// computed truth, not the model's claims.
func (e *Engine) enforce(insight *Insight, cat Category, c computedAnalysis) {
        // 1. Severity — deterministic always wins.
        if insight.Severity != c.severity {
                insight.Violations = append(insight.Violations, Violation{
                        Kind: "severity", LLMValue: string(insight.Severity),
                        Enforced: string(c.severity),
                        Detail:   "severity overridden by deterministic comparison",
                })
                insight.Severity = c.severity
        }

        // 2. Reference — built from threshold metadata; the model's is never trusted.
        //    Only enforce when the category actually declares source URLs.
        if c.reference != "" {
                if !referenceMatches(insight.Reference, cat) {
                        insight.Violations = append(insight.Violations, Violation{
                                Kind: "reference", LLMValue: insight.Reference,
                                Enforced: c.reference,
                                Detail:   "citation did not match any provided source URL; replaced",
                        })
                }
                insight.Reference = c.reference
        }

        // 3. Suggested goal — target/unit/metric must come from a real threshold.
        if insight.SuggestedGoal != nil {
                if v := enforceGoal(insight.SuggestedGoal, cat, c); v != nil {
                        insight.Violations = append(insight.Violations, *v)
                }
        }

        // 4. Numbers in prose — best-effort trace check.
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

func referenceMatches(ref string, cat Category) bool {
        for _, t := range cat.Thresholds {
                if t.SourceURL != "" && strings.Contains(ref, t.SourceURL) {
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
                *g = SuggestedGoal{} // neutralize a goal we can't defend
                return &Violation{Kind: "goal", LLMValue: orig,
                        Detail: "suggested goal referenced an unknown metric; dropped"}
        }

        // The only defensible targets are the threshold bounds.
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

// renderText is the deterministic narration used as a fallback in strict mode
// (and when the LLM returns unparseable output). Blander than the model's
// prose, but every number is correct by construction.
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
// Number tracing in prose (heuristic)
// ---------------------------------------------------------------------------

var numRe = regexp.MustCompile(`-?[\d,]+(?:\.\d+)?`)

// unknownNumbers returns numeric tokens in text that don't correspond to any
// known quantity (actual values or threshold bounds).
//
// HONEST CAVEAT: this is heuristic. Free prose legitimately contains numbers
// that aren't metrics ("over the last 30 days", "top 3 vendors"), so it will
// produce false positives and is a *flagging* signal, not a hard guarantee.
// The fully reliable guarantees in this file are severity, reference, and goal
// — those are structured and enforced unconditionally. For prose, the real
// lever is strict mode, which discards the model's text in favor of renderText.
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

// truncate cuts a string to maxLen, appending "..." if truncated.
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
        // Clean JSON markers
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

        // Extract related metrics
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
