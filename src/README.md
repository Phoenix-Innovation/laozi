# Laozi — Constrained-Reasoning Insight Engine

[![Go Reference](https://pkg.go.dev/badge/github.com/Phoenix-Innovation/laozi.svg)](https://pkg.go.dev/github.com/Phoenix-Innovation/laozi)

> **The LLM has limited authority — a property of the system, not a hope about the prompt.**

Laozi is a Go library that generates LLM-powered insights while **guaranteeing**
that structured fields (severity, citation, goal) reflect deterministic truth,
not the model's claims. Every deviation the LLM produces is caught, corrected,
and recorded in an audit trail.

---

## Install

```bash
go get github.com/Phoenix-Innovation/laozi@v0.2.0
```

Requires **Go 1.21+**. Zero external dependencies.

---

## Quick Start

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Phoenix-Innovation/laozi"
)

// Implement laozi.LLMClient with your provider (OpenAI, Azure, local, etc.)
type myLLM struct{ /* api key, http client, ... */ }

func (m myLLM) Chat(ctx context.Context, system, user string) (string, error) {
	// call your LLM here
	return `{"insight":{"text":"...","severity":"success","reference":"..."}}`, nil
}

func main() {
	engine := laozi.New(
		laozi.WithLLM(myLLM{}),
		// laozi.WithRAG(store),     // optional vector DB
		// laozi.WithStrict(true),   // replace prose on number violations
	)

	engine.AddCategory(laozi.Category{
		ID:   "glucose",
		Name: "Blood Glucose",
		Thresholds: []laozi.Threshold{{
			Metric:    "fasting_glucose",
			Min:       70,
			Max:       99,
			Unit:      "mg/dL",
			Source:    "ADA",
			SourceURL: "https://diabetes.org/diagnosis",
		}},
	})

	insights, err := engine.Analyze(context.Background(), map[string]float64{
		"fasting_glucose": 112.0, // above max → severity enforced to "warning"
	})
	if err != nil {
		log.Fatal(err)
	}

	for _, ins := range insights {
		fmt.Printf("Severity: %s (guaranteed)\n", ins.Severity)
		fmt.Printf("Reference: %s (guaranteed)\n", ins.Reference)
		for _, v := range ins.Violations {
			fmt.Printf("  [%s] LLM said %q → enforced %q\n", v.Kind, v.LLMValue, v.Enforced)
		}
	}
}
```

A complete runnable example lives in
[`example_test.go`](example_test.go) and runs as part of `go test`.

---

## Enforcement Guarantees

The enforcement layer runs **after** the LLM responds and **before** the result
is returned. On any conflict, deterministic truth wins:

| Guarantee | Type | What happens |
|---|---|---|
| **Severity** | Unconditional | `value < min` → `warning`, period. The LLM cannot override arithmetic. |
| **Reference** | Unconditional | Built from `Threshold.Source` + `Threshold.SourceURL`. The model's citation is never trusted. |
| **Suggested Goal** | Unconditional | Target/unit/comparison aligned to the relevant threshold bound. Unknown metrics are dropped. |
| **Numbers in Prose** | Heuristic | Flags numbers not traceable to data or thresholds. In **strict mode**, replaces the entire narration. |

Every correction is recorded as a `Violation`:

```go
type Violation struct {
    Kind     string // "severity" | "reference" | "goal" | "number" | "parse"
    LLMValue string // what the model returned
    Enforced string // what the engine used instead
    Detail   string // human-readable explanation
}
```

An empty `insight.Violations` slice means the LLM was fully compliant.

---

## Pipeline

```
compute ground truth  →  call LLM  →  parse JSON  →  enforce 4 points  →  return Insight
       (Go)               (you)        (Go)             (Go)                  ↑
                                          ↓                              Violations attached
                                    parse fails?
                                    strict → deterministic fallback
                                    lax    → error
```

---

## API Surface

### Engine

```go
engine := laozi.New(opts ...Option)                              // construct
engine.AddCategory(cat Category)                                 // register thresholds
engine.AddCategories(cats []Category)                            // bulk register
engine.Analyze(ctx, metrics map[string]float64) ([]Insight, error)  // all categories
engine.AnalyzeCategory(ctx, id string, metrics) (*Insight, error)   // single category
engine.SetContext(key string, value interface{})                  // goroutine-safe
```

### Options

```go
laozi.WithLLM(client LLMClient)              // required — your LLM adapter
laozi.WithRAG(store RAGStore)                // optional — vector DB for Tier 2
laozi.WithStrict(bool)                       // optional — replace prose on number violations
laozi.WithContext(key string, value any)     // optional — domain context injected into prompts
```

### Interfaces you implement

```go
// LLMClient — the only required adapter. Wrap OpenAI, Azure, Ollama, etc.
type LLMClient interface {
    Chat(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// RAGStore — optional. Plug in pgvector, Pinecone, Weaviate, etc.
type RAGStore interface {
    Search(ctx context.Context, query string, limit int) ([]RAGResult, error)
}
```

### Types

| Type | Purpose |
|---|---|
| `Category` | Groups metrics, thresholds, and optional RAG query |
| `Threshold` | Min/max bounds, unit, source, source URL for one metric |
| `Insight` | Generated output — text, severity, reference, goal, violations |
| `Violation` | One correction the engine made to the LLM's output |
| `SuggestedGoal` | Actionable target (metric, value, comparison) |
| `Severity` | `"warning"` / `"success"` / `"info"` |
| `RAGResult` | Content + source from vector search |

---

## Strict Mode

```go
engine := laozi.New(
    laozi.WithLLM(client),
    laozi.WithStrict(true),  // ← this
)
```

In strict mode:
- **Number violations** → LLM prose replaced with deterministic template
- **Parse failures** → deterministic fallback instead of error
- The LLM is cut out entirely the moment it steps out of line

---

## Tests

```bash
go test ./... -race -count=1 -v
```

**18 tests**, all passing with `-race`:

| Test | What it proves |
|---|---|
| 14-row truth table | Every enforcement path: severity override, bogus citation, goal correction, number tracing, parse failure, strict fallback, educational, multi-metric |
| `TestAnalyzeAllCategories` | Full pipeline with compliant mock → zero violations |
| `TestAnalyzeEnforcesEveryCategory` | Enforcement runs on every category in `Analyze()` |
| `TestAnalyzeParallelRace` | Concurrent `SetContext` + `Analyze` under `-race` |
| `Example` | Runnable godoc example — compiles, executes, output verified |

---

## Releasing

```bash
git tag -a v0.2.0 -m "v0.2.0: enforcement layer"
git push origin v0.2.0
```

After pushing the tag, `pkg.go.dev` picks it up automatically.
Consumers install with:

```bash
go get github.com/Phoenix-Innovation/laozi@v0.2.0
```

---

## File Layout

```
.
├── go.mod                          # module github.com/Phoenix-Innovation/laozi
├── laozi.go                        # all types, engine, pipeline, enforcement (746 lines)
├── enforce.go                      # package declaration only (logic integrated into laozi.go)
├── enforce_test.go                 # 14-row enforcement truth table
├── enforce_concurrency_test.go     # race-condition + parallel tests
├── example_test.go                 # runnable Example() for godoc
└── README.md                       # this file
```

---

## License

MIT
