// Package laozi provides an LLM-powered insight generation engine with
// hallucination prevention through dual-constraint architecture.
package laozi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"
)

// ================================================================================
// TYPES
// ================================================================================

// Severity levels for insights
type Severity string

const (
	SeverityWarning Severity = "warning"
	SeveritySuccess Severity = "success"
	SeverityInfo    Severity = "info"
)

// Threshold defines a metric guideline with source attribution
type Threshold struct {
	Metric    string  // e.g., "current_ratio", "glucose"
	Min       float64 // Minimum acceptable value
	Max       float64 // Maximum acceptable value
	Unit      string  // e.g., "ratio", "mg/dL"
	Source    string  // e.g., "CFA Institute", "ADA"
	SourceURL string  // URL for reference
}

// Category represents an analysis category
type Category struct {
	ID          string
	Name        string
	Thresholds  []Threshold
	Educational bool   // If true, generates "Did You Know" tips
	RAGQuery    string // Optional query for RAG context
}

// Insight is the generated output
type Insight struct {
	CategoryID string
	Text       string
	Severity   Severity
	Reference  string
	Metadata   map[string]string
}

// RAGDocument represents a retrieved document
type RAGDocument struct {
	Content    string
	Source     string
	SourceURL  string
	Similarity float64
}

// RAGStore interface for vector database
type RAGStore interface {
	Search(ctx context.Context, query string, topK int) ([]RAGDocument, error)
}

// LLMClient interface for LLM calls
type LLMClient interface {
	Chat(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// Logger interface for observability
type Logger interface {
	Log(step, category, message string, data map[string]interface{})
}

// ================================================================================
// ENGINE
// ================================================================================

// Engine is the main Laozi insight generator
type Engine struct {
	categories map[string]Category
	llm        LLMClient
	rag        RAGStore
	context    map[string]interface{}
	logger     Logger

	// Templates (parsed once)
	systemTpl     *template.Template
	metricTpl     *template.Template
	educationalTpl *template.Template
	regenTpl      *template.Template
}

// New creates a new Laozi engine
func New(llm LLMClient, rag RAGStore) *Engine {
	e := &Engine{
		categories: make(map[string]Category),
		context:    make(map[string]interface{}),
		llm:        llm,
		rag:        rag,
	}

	// Parse templates at init
	e.systemTpl = template.Must(template.New("system").Parse(SystemPromptTemplate))
	e.metricTpl = template.Must(template.New("metric").Parse(MetricPromptTemplate))
	e.educationalTpl = template.Must(template.New("educational").Parse(EducationalPromptTemplate))
	e.regenTpl = template.Must(template.New("regen").Parse(RegenerationPromptTemplate))

	// Set logger based on config
	if LogEnabled {
		e.logger = &ConsoleLogger{}
	}

	return e
}

// SetLogger overrides the default logger
func (e *Engine) SetLogger(l Logger) {
	e.logger = l
}

// AddContext adds domain context
func (e *Engine) AddContext(key string, value interface{}) {
	e.context[key] = value
}

// AddCategory registers a category
func (e *Engine) AddCategory(cat Category) {
	e.categories[cat.ID] = cat
}

// AddCategories registers multiple categories
func (e *Engine) AddCategories(cats []Category) {
	for _, cat := range cats {
		e.AddCategory(cat)
	}
}

// ================================================================================
// ANALYSIS
// ================================================================================

// AnalyzeAll runs all categories in parallel (uses MaxParallelLLMCalls from config)
func (e *Engine) AnalyzeAll(ctx context.Context, metrics map[string]float64) ([]*Insight, error) {
	var (
		mu       sync.Mutex
		insights []*Insight
		errs     []error
		wg       sync.WaitGroup
		sem      = make(chan struct{}, MaxParallelLLMCalls)
	)

	e.log("BATCH", "all", fmt.Sprintf("Starting %d categories with %d parallel", len(e.categories), MaxParallelLLMCalls), nil)

	for _, cat := range e.categories {
		wg.Add(1)
		go func(c Category) {
			defer wg.Done()
			sem <- struct{}{}        // Acquire
			defer func() { <-sem }() // Release

			insight, err := e.Analyze(ctx, c.ID, metrics)
			mu.Lock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", c.ID, err))
			} else if insight != nil {
				insights = append(insights, insight)
			}
			mu.Unlock()
		}(cat)
	}

	wg.Wait()

	if len(errs) > 0 {
		e.log("BATCH", "all", fmt.Sprintf("Completed with %d errors", len(errs)), nil)
	}

	return insights, nil
}

// Analyze generates an insight for a single category
func (e *Engine) Analyze(ctx context.Context, categoryID string, metrics map[string]float64) (*Insight, error) {
	cat, ok := e.categories[categoryID]
	if !ok {
		return nil, fmt.Errorf("category not found: %s", categoryID)
	}

	// Add timeout from config
	ctx, cancel := context.WithTimeout(ctx, time.Duration(LLMTimeoutSeconds)*time.Second)
	defer cancel()

	return e.analyzeCategory(ctx, cat, metrics)
}

func (e *Engine) analyzeCategory(ctx context.Context, cat Category, metrics map[string]float64) (*Insight, error) {
	var (
		userPrompt       string
		expectedSeverity Severity
	)

	if cat.Educational {
		userPrompt, expectedSeverity = e.buildEducationalPrompt(ctx, cat)
	} else {
		userPrompt, expectedSeverity = e.buildMetricPrompt(cat, metrics)
	}

	e.log("GENERATE", cat.ID, "Calling LLM", map[string]interface{}{"expected": string(expectedSeverity)})

	// STEP 1: Generate
	response, err := e.llm.Chat(ctx, SystemPromptTemplate, userPrompt)
	if err != nil {
		e.log("GENERATE", cat.ID, "LLM failed", map[string]interface{}{"error": err.Error()})
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	// STEP 2: Parse
	insight, err := e.parseResponse(response, cat.ID)
	if err != nil {
		e.log("PARSE", cat.ID, "Parse failed", map[string]interface{}{"error": err.Error()})
		return nil, err
	}
	e.log("PARSE", cat.ID, "OK", map[string]interface{}{"severity": string(insight.Severity)})

	// STEP 3: Validate
	validationErr := e.validateInsight(insight, cat, expectedSeverity)
	if validationErr == nil {
		e.log("VALIDATE", cat.ID, "✓ PASS", nil)
		return insight, nil
	}
	e.log("VALIDATE", cat.ID, "✗ FAIL", map[string]interface{}{"error": validationErr.Error()})

	// STEP 4: Regenerate (up to MaxRetries from config)
	for attempt := 1; attempt <= MaxRetries; attempt++ {
		e.log("REGENERATE", cat.ID, fmt.Sprintf("Attempt %d/%d", attempt, MaxRetries), nil)

		regenPrompt := e.buildRegenPrompt(cat, metrics, expectedSeverity, validationErr.Error())
		response, err = e.llm.Chat(ctx, "You are regenerating a failed insight. Follow instructions EXACTLY.", regenPrompt)
		if err != nil {
			continue
		}

		insight, err = e.parseResponse(response, cat.ID)
		if err != nil {
			continue
		}

		validationErr = e.validateInsight(insight, cat, expectedSeverity)
		if validationErr == nil {
			e.log("REGENERATE", cat.ID, "✓ SUCCESS", nil)
			return insight, nil
		}
	}

	e.log("REGENERATE", cat.ID, "✗ EXHAUSTED", map[string]interface{}{"attempts": MaxRetries})
	return nil, fmt.Errorf("validation failed after %d attempts", MaxRetries)
}

// ================================================================================
// PROMPT BUILDING
// ================================================================================

func (e *Engine) buildMetricPrompt(cat Category, metrics map[string]float64) (string, Severity) {
	var guidelines, metricsText, comparison strings.Builder
	expectedSeverity := SeveritySuccess

	for _, t := range cat.Thresholds {
		guidelines.WriteString(fmt.Sprintf("📊 %s: Range %.2f - %.2f %s\n   Source: %s\n   URL: %s\n\n",
			strings.ToUpper(t.Metric), t.Min, t.Max, t.Unit, t.Source, t.SourceURL))

		if val, ok := metrics[t.Metric]; ok {
			metricsText.WriteString(fmt.Sprintf("- %s: %.2f %s\n", t.Metric, val, t.Unit))

			// Pre-compute comparison
			if val < t.Min {
				comparison.WriteString(fmt.Sprintf("%s: %.2f < %.2f (BELOW minimum) → severity = warning\n",
					t.Metric, val, t.Min))
				expectedSeverity = SeverityWarning
			} else if val > t.Max {
				comparison.WriteString(fmt.Sprintf("%s: %.2f > %.2f (ABOVE maximum) → severity = warning\n",
					t.Metric, val, t.Max))
				expectedSeverity = SeverityWarning
			} else {
				comparison.WriteString(fmt.Sprintf("%s: %.2f within [%.2f - %.2f] → severity = success\n",
					t.Metric, val, t.Min, t.Max))
			}
		}
	}

	data := map[string]string{
		"CategoryID":       cat.ID,
		"CategoryName":     cat.Name,
		"Guidelines":       guidelines.String(),
		"Metrics":          metricsText.String(),
		"Comparison":       comparison.String(),
		"ExpectedSeverity": string(expectedSeverity),
	}

	return e.executeTemplate(e.metricTpl, data), expectedSeverity
}

func (e *Engine) buildEducationalPrompt(ctx context.Context, cat Category) (string, Severity) {
	var refs strings.Builder

	if RAGEnabled && e.rag != nil && cat.RAGQuery != "" {
		docs, err := e.rag.Search(ctx, cat.RAGQuery, RAGTopK)
		if err == nil {
			for i, doc := range docs {
				if doc.Similarity >= RAGMinSimilarity {
					refs.WriteString(fmt.Sprintf("[%d] %s\n    Source: %s - %s\n\n",
						i+1, doc.Content, doc.Source, doc.SourceURL))
				}
			}
		}
	}

	data := map[string]string{
		"CategoryID":   cat.ID,
		"CategoryName": cat.Name,
		"References":   refs.String(),
	}

	return e.executeTemplate(e.educationalTpl, data), SeverityInfo
}

func (e *Engine) buildRegenPrompt(cat Category, metrics map[string]float64, expected Severity, validationError string) string {
	metricPrompt, _ := e.buildMetricPrompt(cat, metrics)

	data := map[string]string{
		"Error":            validationError,
		"CategoryID":       cat.ID,
		"Guidelines":       "", // Already in metricPrompt
		"Metrics":          metricPrompt,
		"ExpectedSeverity": string(expected),
	}

	return e.executeTemplate(e.regenTpl, data)
}

func (e *Engine) executeTemplate(tpl *template.Template, data map[string]string) string {
	var buf strings.Builder
	_ = tpl.Execute(&buf, data)
	return buf.String()
}

// ================================================================================
// VALIDATION
// ================================================================================

func (e *Engine) validateInsight(insight *Insight, cat Category, expected Severity) error {
	if insight == nil {
		return fmt.Errorf("nil insight")
	}

	// Structure checks
	if len(insight.Text) < MinInsightTextLen {
		return fmt.Errorf("text too short: %d < %d", len(insight.Text), MinInsightTextLen)
	}

	// Placeholder check
	for _, p := range InvalidPlaceholders {
		if strings.Contains(insight.Text, p) {
			return fmt.Errorf("contains placeholder: %s", p)
		}
	}

	// Severity check
	validSeverity := false
	for _, s := range ValidSeverities {
		if insight.Severity == s {
			validSeverity = true
			break
		}
	}
	if !validSeverity {
		return fmt.Errorf("invalid severity: %s", insight.Severity)
	}

	// Reference checks
	if RequireReference && insight.Reference == "" {
		return fmt.Errorf("missing reference")
	}
	if RequireURL && !strings.Contains(insight.Reference, "http") {
		return fmt.Errorf("reference missing URL")
	}

	// Semantic check: severity must match expected (for metric categories)
	if EnforceSeverityMatch && !cat.Educational && insight.Severity != expected {
		return fmt.Errorf("severity mismatch: got %s, expected %s", insight.Severity, expected)
	}

	return nil
}

// ================================================================================
// PARSING
// ================================================================================

func (e *Engine) parseResponse(response, categoryID string) (*Insight, error) {
	// Extract JSON from response
	jsonRegex := regexp.MustCompile(`\{[\s\S]*"insight"[\s\S]*\}`)
	match := jsonRegex.FindString(response)
	if match == "" {
		return nil, fmt.Errorf("no JSON found in response")
	}

	var result struct {
		Insight struct {
			Text      string `json:"text"`
			Severity  string `json:"severity"`
			Reference string `json:"reference"`
		} `json:"insight"`
	}

	if err := json.Unmarshal([]byte(match), &result); err != nil {
		return nil, fmt.Errorf("JSON parse error: %w", err)
	}

	return &Insight{
		CategoryID: categoryID,
		Text:       result.Insight.Text,
		Severity:   Severity(result.Insight.Severity),
		Reference:  result.Insight.Reference,
		Metadata:   make(map[string]string),
	}, nil
}

// ================================================================================
// LOGGING
// ================================================================================

func (e *Engine) log(step, category, message string, data map[string]interface{}) {
	if e.logger != nil {
		e.logger.Log(step, category, message, data)
	}
}

// ConsoleLogger prints to stdout
type ConsoleLogger struct{}

func (c *ConsoleLogger) Log(step, category, message string, data map[string]interface{}) {
	var timestamp string
	if LogTimestamps {
		timestamp = time.Now().Format("15:04:05") + " "
	}

	dataStr := ""
	if len(data) > 0 {
		pairs := make([]string, 0, len(data))
		for k, v := range data {
			pairs = append(pairs, fmt.Sprintf("%s=%v", k, v))
		}
		dataStr = " {" + strings.Join(pairs, ", ") + "}"
	}

	fmt.Printf("%s[%s] %s: %s%s\n", timestamp, step, category, message, dataStr)
}

// ================================================================================
// HELPERS
// ================================================================================

// GetAPIKey returns the API key from environment or config
func GetAPIKey() string {
	if key := os.Getenv("LAOZI_API_KEY"); key != "" {
		return key
	}
	return LLMAPIKey
}
