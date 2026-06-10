// Example: using Laozi for financial-metrics analysis.
//
// This runs with NO API key or network: demoLLM (bottom of file) stands in for
// a real model and deliberately returns a wrong severity and a bogus citation,
// so the output shows the enforcement layer correcting them. For real use,
// replace demoLLM{} with laozi.NewDefaultLLMClient() and set LAOZI_API_KEY.
package main

import (
	"context"
	"fmt"

	laozi "github.com/Phoenix-Innovation/laozi"
)

func main() {
	// Seed an in-memory RAG store with financial references (optional Tier 2).
	rag := laozi.NewInMemoryRAG()
	rag.Add(laozi.RAGResult{
		Content:   "A current ratio between 1.5 and 3.0 indicates healthy liquidity. Below 1.0 signals potential solvency issues.",
		Source:    "CFA Institute",
		SourceURL: "https://cfainstitute.org/analysis-guidelines",
	})
	rag.Add(laozi.RAGResult{
		Content:   "SaaS companies typically target 70-80% gross margins. Below 40% may indicate pricing or cost-structure issues.",
		Source:    "McKinsey SaaS Benchmarks",
		SourceURL: "https://mckinsey.com/saas-benchmarks",
	})

	engine := laozi.New(
		laozi.WithLLM(demoLLM{}), // swap for laozi.NewDefaultLLMClient() in production
		laozi.WithRAG(rag),
		laozi.WithConfig(laozi.Config{MaxRetries: 2, MaxParallel: 4}),
		laozi.WithContext("company", map[string]interface{}{
			"name":     "TechCorp Inc.",
			"industry": "Software/SaaS",
			"stage":    "Growth",
		}),
	)

	engine.AddCategories([]laozi.Category{
		{
			ID:   "liquidity",
			Name: "Liquidity Analysis",
			Thresholds: []laozi.Threshold{{
				Metric: "current_ratio", Min: 1.5, Max: 3.0, Unit: "ratio",
				Source: "CFA Institute", SourceURL: "https://cfainstitute.org/analysis-guidelines",
			}},
			RAGQuery: "current ratio liquidity",
		},
		{
			ID:   "profitability",
			Name: "Profitability Analysis",
			Thresholds: []laozi.Threshold{{
				Metric: "gross_margin", Min: 40.0, Max: 80.0, Unit: "%",
				Source: "McKinsey SaaS Benchmarks", SourceURL: "https://mckinsey.com/saas-benchmarks",
			}},
			RAGQuery: "gross margin profitability",
		},
		{
			ID:   "runway",
			Name: "Cash Runway",
			Thresholds: []laozi.Threshold{{
				Metric: "runway_months", Min: 6, Max: 24, Unit: "months",
				Source: "YC Startup Guidelines", SourceURL: "https://ycombinator.com/library",
			}},
		},
	})

	metrics := map[string]float64{
		"current_ratio": 1.2, // below 1.5  -> warning
		"gross_margin":  65,  // within band -> success
		"runway_months": 4,   // below 6    -> warning
	}

	insights, err := engine.Analyze(context.Background(), metrics)
	if err != nil {
		fmt.Println("analyze error:", err)
		return
	}

	fmt.Println("\n=== LAOZI FINANCIAL INSIGHTS ===")
	for _, in := range insights {
		fmt.Printf("\n[%s] %s\n", in.Severity, in.CategoryID)
		fmt.Printf("  %s\n", in.Text)
		fmt.Printf("  ref: %s\n", in.Reference)
		for _, v := range in.Violations { // audit trail: what the LLM tried vs. what was enforced
			fmt.Printf("  enforced %s: %q -> %q\n", v.Kind, v.LLMValue, v.Enforced)
		}
	}
}

// demoLLM stands in for a real model so the example runs offline. It returns a
// deliberately wrong severity ("success") and a bogus citation; the engine's
// enforcement layer corrects both and records the corrections in Violations.
type demoLLM struct{}

func (demoLLM) Chat(_ context.Context, _, _ string) (string, error) {
	return `{"insight":{"text":"This metric was reviewed against the registered guideline.","severity":"success","reference":"Unverified Source - https://made-up.example"}}`, nil
}
