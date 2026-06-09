// Example: Using Laozi for health metrics analysis
package main

import (
	"context"
	"encoding/json"
	"fmt"

	laozi "github.com/Phoenix-Innovation/laozi"
)

func main() {
	// Create RAG store and seed with medical references
	rag := laozi.NewInMemoryRAG()
	rag.Add(laozi.RAGResult{
		Content:   "Adults should aim for 7,000-10,000 steps daily. Studies show 8,000+ steps reduces mortality risk by 51%.",
		Source:    "Lancet Public Health 2022",
		SourceURL: "https://www.thelancet.com/journals/lanpub/article/PIIS2468-2667(21)00302-9/fulltext",
	})
	rag.Add(laozi.RAGResult{
		Content:   "Fasting glucose below 100 mg/dL is normal. 100-125 mg/dL indicates prediabetes. 126+ indicates diabetes.",
		Source:    "American Diabetes Association",
		SourceURL: "https://diabetes.org/about-diabetes/diagnosis",
	})
	rag.Add(laozi.RAGResult{
		Content:   "Did you know? Eating 25-30g of fiber daily can reduce heart disease risk by 30%.",
		Source:    "Harvard Health",
		SourceURL: "https://www.health.harvard.edu/nutrition",
	})

	// Create engine with functional options
	engine := laozi.New(
		laozi.WithLLM(laozi.NewDefaultLLMClient()),
		laozi.WithRAG(rag),
		laozi.WithConfig(laozi.Config{MaxRetries: 2, MaxParallel: 4}),
		laozi.WithContext("patient", map[string]interface{}{
			"age":    45,
			"gender": "male",
		}),
	)

	// Define health metric categories with thresholds
	engine.AddCategories([]laozi.Category{
		{
			ID:   "activity",
			Name: "Physical Activity",
			Thresholds: []laozi.Threshold{{
				Metric:    "steps",
				Min:       8000,
				Max:       15000,
				Unit:      "steps/day",
				Source:    "Lancet Public Health 2022",
				SourceURL: "https://www.thelancet.com/journals/lanpub/article/PIIS2468-2667(21)00302-9/fulltext",
			}},
			RAGQuery: "daily steps physical activity",
		},
		{
			ID:   "glucose",
			Name: "Metabolic Health",
			Thresholds: []laozi.Threshold{{
				Metric:    "fasting_glucose",
				Min:       70,
				Max:       99,
				Unit:      "mg/dL",
				Source:    "American Diabetes Association",
				SourceURL: "https://diabetes.org/about-diabetes/diagnosis",
			}},
			RAGQuery: "fasting glucose diabetes",
		},
		{
			ID:   "blood-pressure",
			Name: "Blood Pressure",
			Thresholds: []laozi.Threshold{
				{
					Metric:    "systolic_bp",
					Min:       90,
					Max:       119,
					Unit:      "mmHg",
					Source:    "Harvard Health Publishing",
					SourceURL: "https://www.health.harvard.edu/heart-health/reading-the-new-blood-pressure-guidelines",
				},
				{
					Metric:    "diastolic_bp",
					Min:       60,
					Max:       79,
					Unit:      "mmHg",
					Source:    "Harvard Health Publishing",
					SourceURL: "https://www.health.harvard.edu/heart-health/reading-the-new-blood-pressure-guidelines",
				},
			},
		},
		{
			ID:          "nutrition-tips",
			Name:        "Nutrition Tips",
			Educational: true,
			RAGQuery:    "nutrition dietary guidelines fiber",
		},
	})

	// Patient's actual metrics
	metrics := map[string]float64{
		"steps":           5200, // Below 8000 -> warning
		"fasting_glucose": 108,  // Above 99 -> warning (prediabetes)
		"systolic_bp":     128,  // Above 119 -> warning
		"diastolic_bp":    82,   // Above 79 -> warning
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
	fmt.Println("                    LAOZI HEALTH INSIGHTS                       ")
	fmt.Println("═══════════════════════════════════════════════════════════════")

	for _, insight := range insights {
		pretty, _ := json.MarshalIndent(insight, "", "  ")
		fmt.Printf("\n%s\n", pretty)
	}
}
