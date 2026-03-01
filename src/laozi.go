// Package laozi provides a dual-constraint insight generation engine.
// It prevents LLM hallucinations by combining hardcoded thresholds (Tier 1)
// with optional RAG context (Tier 2).
//
// Usage:
//
//	engine := laozi.New(
//		laozi.WithLLM("https://api.openai.com/v1", "your-api-key", "gpt-4o"),
//		laozi.WithRAG(myRAGStore), // optional
//	)
//
//	// Define your domain thresholds
//	engine.AddCategory(laozi.Category{
//		ID:   "revenue",
//		Name: "Revenue Analysis",
//		Thresholds: []laozi.Threshold{
//			{Metric: "monthly_revenue", Min: 100000, Max: 500000, Unit: "USD"},
//		},
//	})
//
//	// Generate insights
//	insights, err := engine.Analyze(ctx, map[string]float64{
//		"monthly_revenue": 75000,
//	})
package laozi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

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

// Insight is the output from analysis
type Insight struct {
	ID             string            `json:"id"`
	CategoryID     string            `json:"category_id"`
	CategoryName   string            `json:"category_name"`
	Text           string            `json:"text"`
	Severity       Severity          `json:"severity"`
	RelatedMetrics []string          `json:"related_metrics"`
	Reference      string            `json:"reference"`
	SuggestedGoal  *SuggestedGoal    `json:"suggested_goal,omitempty"`
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

// RAGStore interface for optional vector database integration
type RAGStore interface {
	// Search returns relevant context for a query
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

// Engine is the main Laozi insight generator
type Engine struct {
	categories map[string]Category
	llm        LLMClient
	rag        RAGStore // optional
	context    map[string]interface{}
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
	return func(e *Engine) {
		e.llm = client
	}
}

// WithRAG enables optional RAG support
func WithRAG(store RAGStore) Option {
	return func(e *Engine) {
		e.rag = store
	}
}

// WithContext adds domain context (e.g., user profile, entity info)
func WithContext(key string, value interface{}) Option {
	return func(e *Engine) {
		e.context[key] = value
	}
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

// SetContext updates engine context
func (e *Engine) SetContext(key string, value interface{}) {
	e.context[key] = value
}

// Analyze generates insights for the given metrics
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

// AnalyzeCategory generates an insight for a specific category
func (e *Engine) AnalyzeCategory(ctx context.Context, categoryID string, metrics map[string]float64) (*Insight, error) {
	cat, ok := e.categories[categoryID]
	if !ok {
		return nil, fmt.Errorf("category not found: %s", categoryID)
	}
	return e.analyzeCategory(ctx, cat, metrics)
}

func (e *Engine) analyzeCategory(ctx context.Context, cat Category, metrics map[string]float64) (*Insight, error) {
	// Build threshold guidelines text
	guidelinesText := e.buildGuidelinesText(cat)

	// Build metrics text with pre-computed comparison
	metricsText, expectedSeverity := e.buildMetricsText(cat, metrics)

	// Get RAG context if available
	var ragContext string
	if e.rag != nil && cat.RAGQuery != "" {
		results, err := e.rag.Search(ctx, cat.RAGQuery, 2)
		if err == nil && len(results) > 0 {
			ragContext = e.buildRAGContext(results)
		}
	}

	// Build prompts
	systemPrompt := e.buildSystemPrompt()
	userPrompt := e.buildUserPrompt(cat, guidelinesText, metricsText, ragContext, expectedSeverity)

	// Call LLM
	response, err := e.llm.Chat(ctx, systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	// Parse response
	return e.parseResponse(response, cat)
}

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

func (e *Engine) buildMetricsText(cat Category, metrics map[string]float64) (string, Severity) {
	var sb strings.Builder
	expectedSeverity := SeveritySuccess

	for _, t := range cat.Thresholds {
		val, ok := metrics[t.Metric]
		if !ok {
			continue
		}

		// Pre-compute comparison
		var status string
		if val < t.Min {
			status = fmt.Sprintf("BELOW minimum (%.2f < %.2f)", val, t.Min)
			expectedSeverity = SeverityWarning
		} else if val > t.Max {
			status = fmt.Sprintf("ABOVE maximum (%.2f > %.2f)", val, t.Max)
			expectedSeverity = SeverityWarning
		} else {
			status = fmt.Sprintf("WITHIN range (%.2f - %.2f)", t.Min, t.Max)
		}

		sb.WriteString(fmt.Sprintf("- %s: %.2f %s → %s\n", t.Metric, val, t.Unit, status))
	}

	if cat.Educational {
		expectedSeverity = SeverityInfo
	}

	return sb.String(), expectedSeverity
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

SEVERITY:
- "warning" = value OUTSIDE the threshold range
- "success" = value WITHIN the threshold range  
- "info" = educational tip (for educational categories)

Respond with ONLY valid JSON.`
}

func (e *Engine) buildUserPrompt(cat Category, guidelines, metrics, ragContext string, severity Severity) string {
	contextJSON, _ := json.Marshal(e.context)

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
		cat.Thresholds[0].Metric)
}

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
