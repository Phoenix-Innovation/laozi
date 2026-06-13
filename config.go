package laozi

import "time"

// ================================================================================
// SOURCE OF TRUTH CONFIGURATION
// Every build-time default lives here. Other files read these values instead of
// hardcoding their own, so changing a setting here changes it everywhere.
// Runtime overrides are layered on top: DefaultLLMOption helpers for the client,
// RAGOption helpers for the in-memory store, and WithConfig for the engine — each
// falls back to the constants below when not set.
// ================================================================================

// LLM configuration. Consumed by NewDefaultLLMClient (llm.go).
const (
	LLMModel       = "gpt-4o-mini" // model name; override per-client with WithModel
	LLMEndpoint    = "https://api.openai.com/v1/chat/completions"
	LLMAPIKey      = ""  // fallback only; prefer the LAOZI_API_KEY env var
	LLMTemperature = 0.3 // lower = more deterministic
	LLMMaxTokens   = 500 // max tokens per insight
	LLMTopP        = 0.9 // nucleus sampling
)

// LLMTimeout is the per-request HTTP timeout for the default client.
const LLMTimeout = 30 * time.Second

// MaxParallelLLMCalls is the default ceiling on concurrent category analyses.
// Consumed by Config.defaults (laozi.go) as the MaxParallel default.
const MaxParallelLLMCalls = 35

// RAG configuration. Consumed by NewInMemoryRAG (rag.go) and Config.defaults.
//
// NOTE: RAGMinSimilarity is intentionally low because the bundled simpleEmbedding
// is a bag-of-words hash — cosine similarities between short texts rarely exceed
// ~0.3, so a 0.7 cutoff effectively disables retrieval. Swap in a real embedding
// model (e.g. text-embedding-3-small) and raise this to 0.5+ for production.
const (
	RAGTopK          = 3
	RAGEmbeddingDim  = 384
	RAGMinSimilarity = 0.15
)

// Validation configuration. Consumed by Config.defaults and validate (laozi.go).
const (
	MaxRetries        = 2  // regeneration attempts on validation failure
	MinInsightTextLen = 20 // minimum insight text length
	RequireReference  = true
)

// RequireApproval gates DSL-created artifacts behind human review by default.
// Consumed by ProposeCategory (draft.go); override per-engine with
// WithConfig(Config{AutoApprove: true}).
const RequireApproval = true

// ClassifierHistoryWindow is how many recent conversation lines the Layer-1
// LLM classifier sees for context. Consumed by Classifier.Classify (classifier.go).
const ClassifierHistoryWindow = 4

// InvalidPlaceholders are substrings that, if present in LLM output text,
// indicate an unfilled template and fail validation. Consumed by Config.defaults.
var InvalidPlaceholders = []string{
	"[INSERT", "[PLACEHOLDER", "{{",
	"[VALUE]", "[METRIC]", "[TARGET]", "[UNIT]", "[Source]", "[URL]",
}

// ValidSeverities is the set of severities an LLM response may declare.
// Consumed by parseResponse (laozi.go).
var ValidSeverities = []Severity{SeverityWarning, SeveritySuccess, SeverityInfo, SeverityUnavailable}

// PromptVersion identifies the prompt-template generation for the audit trail.
// Bump it whenever the prompt templates change in a way that affects output.
const PromptVersion = "metric/v2"

// DefaultSafeAdvice is the fallback redirect substituted when narration is
// sanitized (e.g. a clinical directive is removed). Deployments should override
// it per category (Category.SafeAdvice) or per engine (WithSafeAdvice) — e.g. a
// clinical deployment would use "Contact your doctor right away."
const DefaultSafeAdvice = "If anything here is concerning, please consult a qualified professional."

// ================================================================================
// PROMPT TEMPLATES (text/template syntax; rendered in laozi.go)
// ================================================================================

const SystemPromptTemplate = `You are an insight generator using the Laozi dual-constraint system.
You MUST generate exactly ONE insight based on the provided thresholds and data.

RULES:
1. Use ONLY the thresholds and references provided
2. Never invent data or thresholds
3. Compare actual values to thresholds explicitly
4. Include the source URL in your reference
5. Use ONLY numbers from the provided data and thresholds in your narrative
6. The comparison result for each metric (ABOVE / BELOW / WITHIN range) is
   COMPUTED FOR YOU in "ACTUAL VALUES WITH COMPARISON". State it EXACTLY as
   given. NEVER describe a value as below/under when it is ABOVE the range, or
   as within/normal when it is OUTSIDE the range.
7. Provide informational findings only. You MAY advise contacting a qualified
   professional. You MUST NOT instruct the user to start, stop, change, adjust,
   skip, or take any medication, dose, treatment, or other clinical action.

SEVERITY:
- "warning" = value OUTSIDE the threshold range
- "success" = value WITHIN the threshold range
- "info" = educational tip (for educational categories)

Respond with ONLY valid JSON.`

const MetricPromptTemplate = `CATEGORY: {{.CategoryID}} ({{.CategoryName}})

GUIDELINES (Tier 1 - Mandatory):
{{.Guidelines}}
{{.RAGContext}}
CONTEXT:
{{.Context}}

ACTUAL VALUES WITH COMPARISON:
{{.Comparison}}

EXPECTED SEVERITY: {{.ExpectedSeverity}}

OUTPUT (use severity="{{.ExpectedSeverity}}"):
{
  "insight": {
    "text": "[State the value and its comparison result EXACTLY as given above (above/below/within range); do NOT contradict it. Informational only - do not instruct any medication or treatment change.]",
    "severity": "{{.ExpectedSeverity}}",
    "reference": "[Source from guidelines] - [URL]",
    "suggested_goal": { "metric": "{{.MetricName}}", "target": [threshold_value], "unit": "[unit]", "comparison": "gte", "description": "..." }
  }
}`

const EducationalPromptTemplate = `CATEGORY: {{.CategoryID}} ({{.CategoryName}})
TYPE: Educational "Did You Know" tip

CONTEXT:
{{.Context}}

{{.RAGContext}}
OUTPUT (severity MUST be "info"):
{
  "insight": {
    "text": "Did you know? [educational fact]",
    "severity": "info",
    "reference": "[Source] - [URL]"
  }
}`

const RegenerationPromptTemplate = `

PREVIOUS ATTEMPT FAILED VALIDATION: {{.Error}}
Please fix the issue above and regenerate a valid insight.`
