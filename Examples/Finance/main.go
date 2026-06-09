// Example: Using Laozi for financial metrics analysis
package main

import (
	"context"
	"fmt"
	"log"

	laozi "github.com/Phoenix-Innovation/laozi"
)

func main() {
	// Create RAG store and seed with financial references
	rag := laozi.NewInMemoryRAG()
	rag.Add(laozi.RAGResult{
		Content:   "A current ratio between 1.5 and 3.0 indicates healthy liquidity. Below 1.0 signals potential solvency issues.",
		Source:    "CFA Institute",
		SourceURL: "https://cfainstitute.org/analysis-guidelines",
	})
	rag.Add(laozi.RAGResult{
		Content:   "SaaS companies typically target 70-80% gross margins. Below 40% may indicate pricing or cost structure issues.",
		Source:    "McKinsey SaaS Benchmarks",
		SourceURL: "https://mckinsey.com/saas-benchmarks",
	})
	rag.Add(laozi.RAGResult{
		Content:   "Did you know? Companies with 6+ months of cash runway have 3x higher survival rates during economic downturns.",
		Source:    "Harvard Business Review",
		SourceURL: "https://hbr.org/finance",
	})

	// Create engine with functional options
	engine := laozi.New(
		laozi.WithLLM(laozi.NewDefaultLLMClient()),
		laozi.WithRAG(rag),
		laozi.WithConfig(laozi.Config{MaxRetries: 2, MaxParallel: 4}),
		laozi.WithContext("company", map[string]interface{}{
			"name":     "TechCorp Inc.",
			"industry": "Software/SaaS",
			"stage":    "Growth",
		}),
	)

	// Define financial categories with thresholds
	engine.AddCategories([]laozi.Category{
		{
			ID:   "liquidity",
			Name: "Liquidity Analysis",
			Thresholds: []laozi.Threshold{{
				Metric:    "current_ratio",
				Min:       1.5,
				Max:       3.0,
				Unit:      "ratio",
				Source:    "CFA Institute",
				SourceURL: "https://cfainstitute.org/analysis-guidelines",
			}},
			RAGQuery: "current ratio liquidity",
		},
		{
			ID:   "profitability",
			Name: "Profitability Analysis",
			Thresholds: []laozi.Threshold{{
				Metric:    "gross_margin",
				Min:       40.0,
				Max:       80.0,
				Unit:      "%",
				Source:    "McKinsey SaaS Benchmarks",
				SourceURL: "https://mckinsey.com/saas-benchmarks",
			}},
			RAGQuery: "gross margin profitability",
		},
		{
			ID:   "runway",
			Name: "Cash Runway",
			Thresholds: []laozi.Threshold{{
				Metric:    "runway_months",
				Min:       6,
				Max:       24,
				Unit:      "months",
				Source:    "YC Startup Guidelines",
				SourceURL: "https://ycombinator.com/library",
			}},
		},
		{
			ID:          "tips",
			Name:        "Financial Tips",
			Educational: true,
			RAGQuery:    "cash management financial best practices",
		},
	})

	// Company's actual metrics
	metrics := map[string]float64{
		"current_ratio": 1.2, // Below 1.5 -> warning
		"gross_margin":  65,  // Within 40-80 -> success
		"runway_months": 4,   // Below 6 -> warning
	}

	// Generate insights
	ctx := context.Background()
	insights, err := engine.Analyze(ctx, metrics)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("\n═══════════════════════════════════════════════════════════════")
	fmt.Println("                   LAOZI FINANCIAL INSIGHTS                     ")
	fmt.Println("═══════════════════════════════════════════════════════════════")

	for _, insight := range insights {
		fmt.Printf("\n[%s] %s\n", insight.Severity, insight.CategoryID)
		fmt.Printf("  %s\n", insight.Text)
		fmt.Printf("  Ref: %s\n", insight.Reference)
	}
}
