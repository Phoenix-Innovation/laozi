package laozi

// ================================================================================
// BUILD-TIME CONFIGURATION
// All settings here are compiled into the binary. No runtime configuration.
// ================================================================================

// LLM Configuration
const (
	// Model settings
	LLMModel       = "gpt-4o-mini"           // or "claude-3-sonnet", custom model name
	LLMEndpoint    = "https://api.openai.com/v1/chat/completions"
	LLMAPIKey      = ""                      // Set via environment: LAOZI_API_KEY
	
	// Generation parameters
	LLMTemperature = 0.3                     // Lower = more deterministic
	LLMMaxTokens   = 500                     // Max tokens per insight
	LLMTopP        = 0.9                     // Nucleus sampling
	
	// Parallelism
	MaxParallelLLMCalls = 35                 // Concurrent LLM requests
	LLMTimeoutSeconds   = 30                 // Per-request timeout
)

// RAG Configuration
const (
	RAGTopK            = 3                   // Number of documents to retrieve
	RAGEmbeddingDim    = 384                 // Embedding vector dimension
)

// Validation Configuration
const (
	MaxRetries          = 2                  // Regeneration attempts on validation failure
	MinInsightTextLen   = 20                 // Minimum insight text length
	RequireReference    = true               // Insights must have source reference
)

// Logging Configuration
const (
	LogEnabled    = true                     // Enable pipeline logging
	LogLevel      = "info"                   // "debug", "info", "warn", "error"
	LogTimestamps = true                     // Include timestamps in logs
)

// Placeholder strings that indicate invalid LLM output
var InvalidPlaceholders = []string{
	"[VALUE]",
	"[METRIC]",
	"[TARGET]",
	"[UNIT]",
	"[Source]",
	"[URL]",
	"{{VALUE}}",
	"{{METRIC}}",
}

// Valid severity values
var ValidSeverities = []Severity{
	SeverityWarning,
	SeveritySuccess,
	SeverityInfo,
}

// ================================================================================
// PROMPT TEMPLATES (compiled into binary)
// ================================================================================

const SystemPromptTemplate = `You are a precise analytical engine. You MUST generate exactly ONE insight.

RULES:
1. Use ONLY the provided GUIDELINES and VALUES - no external knowledge
2. The severity is PRE-COMPUTED - use the REQUIRED SEVERITY exactly
3. Reference must include source name AND URL from the guidelines
4. Never use placeholders like [VALUE] or [METRIC]

OUTPUT FORMAT:
{"insight": {"text": "...", "severity": "...", "reference": "Source - URL"}}`

const MetricPromptTemplate = `CATEGORY: {{.CategoryID}} ({{.CategoryName}})

GUIDELINES:
{{.Guidelines}}

PATIENT VALUES:
{{.Metrics}}

PRE-COMPUTED COMPARISON:
{{.Comparison}}

REQUIRED SEVERITY: {{.ExpectedSeverity}}

Generate ONE insight comparing the patient value to the guideline threshold.`

const EducationalPromptTemplate = `CATEGORY: {{.CategoryID}} ({{.CategoryName}})

REFERENCES:
{{.References}}

Generate ONE "Did You Know" educational tip using the references above.
Severity MUST be "info".`

const RegenerationPromptTemplate = `REGENERATION REQUIRED - Previous output failed validation.

VALIDATION ERROR: {{.Error}}

CATEGORY: {{.CategoryID}}
GUIDELINES: {{.Guidelines}}
VALUES: {{.Metrics}}
REQUIRED SEVERITY: {{.ExpectedSeverity}}

Generate a VALID insight. Follow instructions EXACTLY.`
