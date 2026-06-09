# Laozi

[![Go Tests](https://github.com/Phoenix-Innovation/laozi/actions/workflows/test.yml/badge.svg)](https://github.com/Phoenix-Innovation/laozi/actions/workflows/test.yml)

A dual-constraint enforcement engine for LLM-generated insights.
The LLM has **limited authority** — that's a property of the system, not a hope about the prompt.

## How It Works

Laozi sits between your LLM and the user. Every insight the LLM generates is
run through deterministic enforcement that **cannot be bypassed by prompt
injection**:

1. **Severity enforcement** — if a metric is outside its threshold, the
   severity is rewritten to match. The LLM cannot claim "success" when the
   data says otherwise.
2. **Citation enforcement** — references must use the pre-registered source
   URL. Invented citations are replaced.
3. **Numeric guardrails** — in strict mode, any number the LLM invents
   (not present in the original data or thresholds) is rejected.
4. **Retry loop** — if the LLM output fails validation (too short, contains
   placeholders, unparseable), the engine retries with the error appended to
   the prompt. Configurable via `Config.MaxRetries` (default 2).
5. **Parallel analysis** — `Analyze()` processes categories concurrently,
   bounded by `Config.MaxParallel` (default 4).

## Install

```bash
go get github.com/Phoenix-Innovation/laozi
```

Requires Go 1.21+.

## Quick Start

```go
package main

import (
	"context"
	"fmt"
	"github.com/Phoenix-Innovation/laozi"
)

func main() {
	// Bring your own LLM — any type that implements laozi.LLM.
	var myLLM laozi.LLM // = your OpenAI / Anthropic / local wrapper

	e := laozi.New(
		laozi.WithLLM(myLLM),
		laozi.WithStrict(true),
		laozi.WithConfig(laozi.Config{
			MaxRetries:  3,
			MaxParallel: 8,
			MinTextLen:  20,
		}),
	)

	e.AddCategory(laozi.Category{
		ID:   "liquidity",
		Name: "Liquidity",
		Thresholds: []laozi.Threshold{{
			Metric: "current_ratio",
			Min:    1.5, Max: 3.0,
			Unit:   "ratio",
			Source: "CFA Institute",
			SourceURL: "https://cfainstitute.org/liquidity",
		}},
	})

	metrics := map[string]float64{"current_ratio": 1.2}
	insights, err := e.Analyze(context.Background(), metrics)
	if err != nil {
		panic(err)
	}
	for _, ins := range insights {
		fmt.Printf("%s: %s — %s\n", ins.CategoryID, ins.Severity, ins.Text)
		// liquidity: warning — ... (severity enforced: 1.2 < 1.5)
	}
}
```

## Core Types

### LLM Interface

```go
type LLM interface {
	Chat(ctx context.Context, system, user string) (string, error)
}
```

Implement this with any provider. The engine calls `Chat` once per category
(plus retries on validation failure).

### Config

```go
type Config struct {
	MaxRetries   int      // LLM retry attempts on validation failure (default 2)
	MaxParallel  int      // concurrent category analyses (default 4)
	MinTextLen   int      // minimum insight text length (0 = disabled)
	Placeholders []string // substrings that invalidate output (default: [INSERT, [PLACEHOLDER, {{)
}
```

Pass via `WithConfig(cfg)`. Zero values use defaults.

### Category & Threshold

```go
type Category struct {
	ID          string
	Name        string
	Thresholds  []Threshold
	Educational bool       // no thresholds to enforce
	Goals       []Goal     // target-based checks
}

type Threshold struct {
	Metric    string
	Min, Max  float64
	Unit      string
	Source    string
	SourceURL string
}
```

### Insight (output)

```go
type Insight struct {
	CategoryID string
	Text       string
	Severity   Severity   // success | info | warning | danger
	Reference  string
	Violations []Violation
}
```

`Violations` logs every enforcement action taken — severity corrections,
citation replacements, numeric rejections. This is an audit trail, not an
error list.

## RAG Support

```go
e.AddRAGResults([]laozi.RAGResult{
	{Content: "The current ratio...", Source: "Annual Report 2024"},
})
```

RAG results are injected into the LLM prompt as grounding context.
Enforcement still applies — RAG doesn't bypass constraints.

## Adaptive Context

```go
e.SetContext("portfolio_size", "large-cap")
e.SetContext("region", "APAC")
```

Key-value pairs appended to every prompt. Thread-safe (uses `sync.RWMutex`).

## Enforcement Details

| Check | Strict Mode | Lax Mode |
|---|---|---|
| Severity vs thresholds | Enforced + violation logged | Enforced + violation logged |
| Citation URL | Replaced with registered source | Replaced with registered source |
| Invented numbers | **Rejected** (text rewritten) | Allowed |
| Parse failure | Fallback to canned text | Fallback to canned text |
| Validation failure | Retry up to MaxRetries, then fallback | Retry up to MaxRetries, then fallback |

## Testing

```bash
go test ./... -race
```

The test suite includes:
- **14-row truth table** covering every severity × enforcement combination
- **Concurrency tests** with `-race` (16 goroutines × 40 iterations)
- **Runnable example** (`Example` function)

## License

MIT
