// Example: Using Laozi for financial metrics analysis
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	laozi "github.com/laozi/plugin"
)

func main() {
	// Create LLM client
	llmClient := laozi.NewOpenAIClient(
		os.Getenv("LLM_ENDPOINT"),
		os.Getenv("LLM_API_KEY"),
		os.Getenv("LLM_MODEL"),
	)

	// Optional: RAG store with financial guidelines
	rag := laozi.NewInMemoryRAG()
	rag.Add(
		"A current ratio between 1.5-3.0 indicates healthy liquidity. Below 1.0 suggests potential cash flow issues.",
		"CFA Institute",
		"https://www.cfainstitute.org/en/membership/professional-development/refresher-readings/financial-analysis-techniques",
		"liquidity",
	)
	rag.Add(
		"Debt-to-equity ratio below 2.0 is generally healthy. Tech companies often operate at 0.5-1.5, while utilities may be higher.",
		"Harvard Business Review",
		"https://hbr.org/topic/financial-analysis",
		"leverage",
	)
	rag.Add(
		"Operating margin varies by industry. SaaS companies target 20-30%, while retail typically sees 5-10%.",
		"McKinsey & Company",
		"https://www.mckinsey.com/capabilities/strategy-and-corporate-finance/our-insights",
		"profitability",
	)

	// Create Laozi engine
	engine := laozi.New(
		laozi.WithLLM(llmClient),
		laozi.WithRAG(rag),
		laozi.WithContext("company", map[string]interface{}{
			"name":     "TechCorp Inc.",
			"industry": "Software/SaaS",
			"stage":    "Growth",
		}),
	)

	// Define financial metric thresholds
	engine.AddCategories([]laozi.Category{
		{
			ID:   "liquidity",
			Name: "Liquidity Analysis",
			Thresholds: []laozi.Threshold{
				{
					Metric:      "current_ratio",
					Min:         1.5,
					Max:         3.0,
					OptimalMin:  1.8,
					OptimalMax:  2.5,
					Unit:        "ratio",
					Source:      "CFA Institute",
					SourceURL:   "https://www.cfainstitute.org/en/membership/professional-development/refresher-readings/financial-analysis-techniques",
					Description: "Current Assets / Current Liabilities. Below 1.0 indicates liquidity risk.",
				},
				{
					Metric:      "quick_ratio",
					Min:         1.0,
					Max:         2.0,
					Unit:        "ratio",
					Source:      "CFA Institute",
					SourceURL:   "https://www.cfainstitute.org/en/membership/professional-development/refresher-readings/financial-analysis-techniques",
					Description: "(Current Assets - Inventory) / Current Liabilities",
				},
			},
			RAGQuery: "liquidity ratio current ratio quick ratio analysis",
		},
		{
			ID:   "leverage",
			Name: "Leverage & Solvency",
			Thresholds: []laozi.Threshold{
				{
					Metric:      "debt_to_equity",
					Min:         0.0,
					Max:         1.5,
					OptimalMin:  0.5,
					OptimalMax:  1.0,
					Unit:        "ratio",
					Source:      "Harvard Business Review",
					SourceURL:   "https://hbr.org/topic/financial-analysis",
					Description: "Total Debt / Total Equity. SaaS companies typically 0.5-1.5",
				},
				{
					Metric:      "interest_coverage",
					Min:         3.0,
					Max:         20.0,
					Unit:        "ratio",
					Source:      "S&P Global",
					SourceURL:   "https://www.spglobal.com/ratings",
					Description: "EBIT / Interest Expense. Below 2.5 is concerning.",
				},
			},
			RAGQuery: "debt equity leverage solvency analysis",
		},
		{
			ID:   "profitability",
			Name: "Profitability Analysis",
			Thresholds: []laozi.Threshold{
				{
					Metric:      "operating_margin",
					Min:         15.0,
					Max:         40.0,
					OptimalMin:  20.0,
					OptimalMax:  30.0,
					Unit:        "%",
					Source:      "McKinsey & Company",
					SourceURL:   "https://www.mckinsey.com/capabilities/strategy-and-corporate-finance/our-insights",
					Description: "Operating Income / Revenue. SaaS benchmark: 20-30%",
				},
				{
					Metric:      "net_margin",
					Min:         10.0,
					Max:         30.0,
					Unit:        "%",
					Source:      "McKinsey & Company",
					SourceURL:   "https://www.mckinsey.com/capabilities/strategy-and-corporate-finance/our-insights",
					Description: "Net Income / Revenue",
				},
			},
			RAGQuery: "operating margin profitability SaaS benchmark",
		},
		{
			ID:   "growth",
			Name: "Growth Metrics",
			Thresholds: []laozi.Threshold{
				{
					Metric:      "revenue_growth_yoy",
					Min:         20.0,
					Max:         100.0,
					OptimalMin:  30.0,
					OptimalMax:  50.0,
					Unit:        "%",
					Source:      "Bessemer Venture Partners",
					SourceURL:   "https://www.bvp.com/atlas/state-of-the-cloud-2024",
					Description: "Year-over-year revenue growth. Growth-stage SaaS: 30-50%",
				},
			},
			RAGQuery: "SaaS revenue growth benchmark",
		},
		{
			ID:          "market-tips",
			Name:        "Market Insights",
			Educational: true,
			Thresholds:  []laozi.Threshold{},
			RAGQuery:    "SaaS market trends valuation multiples",
		},
	})

	// Company's actual metrics
	metrics := map[string]float64{
		"current_ratio":     1.2,  // Below healthy range
		"quick_ratio":       0.9,  // Below 1.0 - concerning
		"debt_to_equity":    2.1,  // Above threshold
		"interest_coverage": 4.5,  // Acceptable
		"operating_margin":  12.0, // Below SaaS benchmark
		"net_margin":        8.0,  // Below threshold
		"revenue_growth_yoy": 45.0, // Strong growth
	}

	// Generate insights
	ctx := context.Background()
	insights, err := engine.Analyze(ctx, metrics)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Output results
	fmt.Println("\n═══════════════════════════════════════════════════════════════")
	fmt.Println("                 LAOZI FINANCIAL INSIGHTS                       ")
	fmt.Println("              TechCorp Inc. (Software/SaaS)                     ")
	fmt.Println("═══════════════════════════════════════════════════════════════")

	for _, insight := range insights {
		pretty, _ := json.MarshalIndent(insight, "", "  ")
		fmt.Printf("\n%s\n", pretty)
	}
}
