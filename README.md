# laozi
Contrainted Reasoning LLM generic plugin- (vibe coded version)
[README(1).md](https://github.com/user-attachments/files/25651682/README.1.md)
# Laozi Go Plugin

**Dual-Constraint Insight Generation Engine**

Laozi is a domain-agnostic insight generation plugin that prevents LLM hallucinations through a two-tier constraint system:

- **Tier 1 (Mandatory)**: Hardcoded thresholds with authoritative sources
- **Tier 2 (Optional)**: RAG-powered additional context

## Philosophy

> *"Constrained reasoning instead of rules"*

Instead of relying on the LLM to "know" thresholds, Laozi explicitly provides them in the prompt along with pre-computed comparisons. The LLM's only job is to generate prose around deterministic results.

## Installation

```bash
go get github.com/laozi/plugin
```

## Quick Start

```go
package main

import (
    "context"
    laozi "github.com/laozi/plugin"
)

func main() {
    // Create engine with LLM client
    engine := laozi.New(
        laozi.WithLLM(laozi.NewOpenAIClient(
            "https://api.openai.com/v1",
            "your-api-key",
            "gpt-4o",
        )),
    )

    // Define domain-specific thresholds
    engine.AddCategory(laozi.Category{
        ID:   "revenue",
        Name: "Revenue Analysis",
        Thresholds: []laozi.Threshold{{
            Metric:    "monthly_revenue",
            Min:       100000,
            Max:       500000,
            Unit:      "USD",
            Source:    "Industry Benchmark",
            SourceURL: "https://example.com/benchmarks",
        }},
    })

    // Analyze metrics
    insights, _ := engine.Analyze(context.Background(), map[string]float64{
        "monthly_revenue": 75000, // Below threshold → warning
    })
}
```

## Core Concepts

### Categories

Categories group related metrics and thresholds:

```go
laozi.Category{
    ID:          "liquidity",           // Unique identifier
    Name:        "Liquidity Analysis",  // Display name
    Thresholds:  []laozi.Threshold{...}, // Metric constraints
    Educational: false,                  // If true, generates "Did You Know" tips
    RAGQuery:    "liquidity ratios",    // Optional: query for RAG context
}
```

### Thresholds

Thresholds define the acceptable range for a metric:

```go
laozi.Threshold{
    Metric:      "current_ratio",  // Metric name (matches input map key)
    Min:         1.5,              // Below this → warning
    Max:         3.0,              // Above this → warning
    OptimalMin:  1.8,              // Optimal range (informational)
    OptimalMax:  2.5,
    Unit:        "ratio",          // Display unit
    Source:      "CFA Institute",  // Authority source
    SourceURL:   "https://...",   // Reference URL (included in insight)
    Description: "Current Assets / Current Liabilities",
}
```

### Severity Levels

| Severity | When Used |
|----------|----------|
| `warning` | Value is OUTSIDE the threshold range |
| `success` | Value is WITHIN the threshold range |
| `info` | Educational categories ("Did You Know" tips) |

## Optional RAG Support

Add additional context from a vector database:

```go
// Built-in in-memory RAG (for simple use cases)
rag := laozi.NewInMemoryRAG()
rag.Add(
    "Healthy companies maintain current ratio between 1.5-3.0...",
    "CFA Institute",
    "https://cfainstitute.org/...",
    "liquidity",
)

engine := laozi.New(
    laozi.WithLLM(llmClient),
    laozi.WithRAG(rag),  // Enable RAG
)
```

### Custom RAG Store

Implement the `RAGStore` interface for production use:

```go
type RAGStore interface {
    Search(ctx context.Context, query string, limit int) ([]RAGResult, error)
}

type RAGResult struct {
    Content   string
    Source    string
    SourceURL string
    Score     float64
}
```

Example with Pinecone, Weaviate, or any vector DB:

```go
type PineconeRAG struct {
    client *pinecone.Client
    index  string
}

func (p *PineconeRAG) Search(ctx context.Context, query string, limit int) ([]laozi.RAGResult, error) {
    // Your implementation
}

engine := laozi.New(
    laozi.WithLLM(llmClient),
    laozi.WithRAG(&PineconeRAG{...}),
)
```

## Context

Add domain context that's included in prompts:

```go
engine := laozi.New(
    laozi.WithLLM(llmClient),
    laozi.WithContext("company", map[string]interface{}{
        "name":     "TechCorp Inc.",
        "industry": "Software/SaaS",
        "stage":    "Growth",
    }),
    laozi.WithContext("period", "Q4 2025"),
)

// Or update later
engine.SetContext("user", map[string]interface{}{"role": "CFO"})
```

## Custom LLM Client

Implement the `LLMClient` interface:

```go
type LLMClient interface {
    Chat(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}
```

The built-in `OpenAIClient` works with any OpenAI-compatible API (OpenAI, Azure, vLLM, Ollama, etc.):

```go
// OpenAI
llm := laozi.NewOpenAIClient("https://api.openai.com/v1", apiKey, "gpt-4o")

// Azure OpenAI
llm := laozi.NewOpenAIClient("https://your-resource.openai.azure.com", apiKey, "gpt-4")

// Local vLLM
llm := laozi.NewOpenAIClient("http://localhost:8000", "", "Qwen/Qwen2.5-72B")

// Ollama
llm := laozi.NewOpenAIClient("http://localhost:11434/v1", "", "llama3")
```

## Domain Examples

### Healthcare

```go
engine.AddCategories([]laozi.Category{
    {
        ID: "blood-pressure",
        Name: "Blood Pressure",
        Thresholds: []laozi.Threshold{
            {Metric: "systolic", Min: 90, Max: 119, Unit: "mmHg", Source: "AHA"},
            {Metric: "diastolic", Min: 60, Max: 79, Unit: "mmHg", Source: "AHA"},
        },
    },
})
```

### Finance

```go
engine.AddCategories([]laozi.Category{
    {
        ID: "profitability",
        Name: "Profitability",
        Thresholds: []laozi.Threshold{
            {Metric: "operating_margin", Min: 15, Max: 40, Unit: "%", Source: "McKinsey"},
            {Metric: "net_margin", Min: 10, Max: 30, Unit: "%", Source: "McKinsey"},
        },
    },
})
```

### IoT / Manufacturing

```go
engine.AddCategories([]laozi.Category{
    {
        ID: "temperature",
        Name: "Equipment Temperature",
        Thresholds: []laozi.Threshold{
            {Metric: "motor_temp", Min: 20, Max: 85, Unit: "°C", Source: "OEM Manual"},
            {Metric: "ambient_temp", Min: 15, Max: 35, Unit: "°C", Source: "ISO 7730"},
        },
    },
})
```

## Output Structure

```go
type Insight struct {
    ID             string            // "category-001"
    CategoryID     string            // "liquidity"
    CategoryName   string            // "Liquidity Analysis"
    Text           string            // Generated insight text
    Severity       Severity          // warning | success | info
    RelatedMetrics []string          // ["current_ratio", "quick_ratio"]
    Reference      string            // "CFA Institute - https://..."
    SuggestedGoal  *SuggestedGoal    // Optional improvement target
    Metadata       map[string]string // Custom metadata
}
```

## How It Prevents Hallucination

1. **Explicit Thresholds**: The LLM receives exact min/max values
2. **Pre-computed Comparison**: We compute "value X is ABOVE/BELOW threshold" before the LLM call
3. **Expected Severity**: We tell the LLM what severity to use based on our comparison
4. **Source Attribution**: Every insight must reference the provided source

Example prompt sent to LLM:

```
GUIDELINES (Tier 1 - Mandatory):
📊 CURRENT_RATIO GUIDELINE:
   Range: 1.50 - 3.00 ratio
   Source: CFA Institute
   URL: https://cfainstitute.org/...

ACTUAL VALUES WITH COMPARISON:
- current_ratio: 1.20 ratio → BELOW minimum (1.20 < 1.50)

EXPECTED SEVERITY: warning
```

## License

MIT License
