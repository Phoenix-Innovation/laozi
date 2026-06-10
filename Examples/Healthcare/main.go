// Example: using Laozi for health-metrics analysis.
//
// Runs offline (no API key/network): demoLLM stands in for a real model and
// returns a wrong severity plus a bogus citation, so the JSON output shows the
// enforcement layer correcting them (see the "violations" field on each
// insight). For real use, replace demoLLM{} with laozi.NewDefaultLLMClient()
// and set LAOZI_API_KEY.
package main

import (
	"context"
	"encoding/json"
	"fmt"

	laozi "github.com/Phoenix-Innovation/laozi"
)

func main() {
	rag := laozi.NewInMemoryRAG()
	rag.Add(laozi.RAGResult{
		Content:   "Adults should aim for 7,000-10,000 steps daily; 8,000+ is associated with lower mortality risk.",
		Source:    "Lancet Public Health 2022",
		SourceURL: "https://www.thelancet.com/journals/lanpub",
	})
	rag.Add(laozi.RAGResult{
		Content:   "Fasting glucose below 100 mg/dL is normal; 100-125 indicates prediabetes; 126+ indicates diabetes.",
		Source:    "American Diabetes Association",
		SourceURL: "https://diabetes.org/about-diabetes/diagnosis",
	})

	engine := laozi.New(
		laozi.WithLLM(demoLLM{}), // swap for laozi.NewDefaultLLMClient() in production
		laozi.WithRAG(rag),
		laozi.WithConfig(laozi.Config{MaxRetries: 2, MaxParallel: 4}),
		laozi.WithContext("patient", map[string]interface{}{
			"age":    45,
			"gender": "male",
		}),
	)

	engine.AddCategories([]laozi.Category{
		{
			ID:   "activity",
			Name: "Physical Activity",
			Thresholds: []laozi.Threshold{{
				Metric: "steps", Min: 8000, Max: 15000, Unit: "steps/day",
				Source: "Lancet Public Health 2022", SourceURL: "https://www.thelancet.com/journals/lanpub",
			}},
			RAGQuery: "daily steps physical activity",
		},
		{
			ID:   "glucose",
			Name: "Metabolic Health",
			Thresholds: []laozi.Threshold{{
				Metric: "fasting_glucose", Min: 70, Max: 99, Unit: "mg/dL",
				Source: "American Diabetes Association", SourceURL: "https://diabetes.org/about-diabetes/diagnosis",
			}},
			RAGQuery: "fasting glucose diabetes",
		},
		{
			ID:   "blood-pressure",
			Name: "Blood Pressure",
			Thresholds: []laozi.Threshold{
				{Metric: "systolic_bp", Min: 90, Max: 119, Unit: "mmHg", Source: "Harvard Health", SourceURL: "https://www.health.harvard.edu/heart-health"},
				{Metric: "diastolic_bp", Min: 60, Max: 79, Unit: "mmHg", Source: "Harvard Health", SourceURL: "https://www.health.harvard.edu/heart-health"},
			},
		},
	})

	metrics := map[string]float64{
		"steps":           5200, // below 8000 -> warning
		"fasting_glucose": 108,  // above 99   -> warning (prediabetes)
		"systolic_bp":     128,  // above 119  -> warning
		"diastolic_bp":    82,   // above 79   -> warning
	}

	insights, err := engine.Analyze(context.Background(), metrics)
	if err != nil {
		fmt.Println("analyze error:", err)
		return
	}

	fmt.Println("\n=== LAOZI HEALTH INSIGHTS ===")
	for _, in := range insights {
		pretty, _ := json.MarshalIndent(in, "", "  ")
		fmt.Printf("\n%s\n", pretty)
	}
}

// demoLLM stands in for a real model so the example runs offline. It returns a
// deliberately wrong severity and a bogus citation; the engine corrects both
// and records the corrections in each insight's Violations.
type demoLLM struct{}

func (demoLLM) Chat(_ context.Context, _, _ string) (string, error) {
	return `{"insight":{"text":"This metric was reviewed against the registered guideline.","severity":"success","reference":"Unverified Source - https://made-up.example"}}`, nil
}
