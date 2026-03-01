// Example: Using Laozi for health metrics analysis
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
		os.Getenv("LLM_ENDPOINT"),  // or "https://api.openai.com/v1"
		os.Getenv("LLM_API_KEY"),
		os.Getenv("LLM_MODEL"), // or "gpt-4o"
	)

	// Optional: Create RAG store with medical references
	rag := laozi.NewInMemoryRAG()
	rag.Add(
		"Adults should aim for 7,000-10,000 steps daily. Studies show 8,000+ steps reduces mortality risk by 51%.",
		"Lancet Public Health 2022",
		"https://www.thelancet.com/journals/lanpub/article/PIIS2468-2667(21)00302-9/fulltext",
		"activity",
	)
	rag.Add(
		"Fasting glucose below 100 mg/dL is normal. 100-125 mg/dL indicates prediabetes. 126+ indicates diabetes.",
		"American Diabetes Association",
		"https://diabetes.org/about-diabetes/diagnosis",
		"glucose",
	)

	// Create Laozi engine
	engine := laozi.New(
		laozi.WithLLM(llmClient),
		laozi.WithRAG(rag), // optional
		laozi.WithContext("patient", map[string]interface{}{
			"age":    45,
			"gender": "male",
		}),
	)

	// Define health metric thresholds (Tier 1 - hardcoded constraints)
	engine.AddCategories([]laozi.Category{
		{
			ID:   "activity",
			Name: "Physical Activity",
			Thresholds: []laozi.Threshold{
				{
					Metric:      "steps",
					Min:         8000,
					Max:         15000,
					OptimalMin:  8000,
					OptimalMax:  10000,
					Unit:        "steps/day",
					Source:      "Lancet Public Health 2022",
					SourceURL:   "https://www.thelancet.com/journals/lanpub/article/PIIS2468-2667(21)00302-9/fulltext",
					Description: "8,000-10,000 steps/day associated with optimal mortality benefit",
				},
			},
			RAGQuery: "daily steps physical activity guidelines",
		},
		{
			ID:   "glucose",
			Name: "Metabolic Health",
			Thresholds: []laozi.Threshold{
				{
					Metric:      "fasting_glucose",
					Min:         70,
					Max:         99,
					OptimalMin:  70,
					OptimalMax:  90,
					Unit:        "mg/dL",
					Source:      "American Diabetes Association",
					SourceURL:   "https://diabetes.org/about-diabetes/diagnosis",
					Description: "Prediabetes: 100-125 mg/dL, Diabetes: ≥126 mg/dL",
				},
			},
			RAGQuery: "fasting glucose diabetes prediabetes",
		},
		{
			ID:   "blood-pressure",
			Name: "Blood Pressure",
			Thresholds: []laozi.Threshold{
				{
					Metric:      "systolic_bp",
					Min:         90,
					Max:         119,
					OptimalMin:  90,
					OptimalMax:  120,
					Unit:        "mmHg",
					Source:      "Harvard Health Publishing",
					SourceURL:   "https://www.health.harvard.edu/heart-health/reading-the-new-blood-pressure-guidelines",
					Description: "Normal: <120/80, Elevated: 120-129/<80, Hypertension: ≥130/80",
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
			Thresholds:  []laozi.Threshold{},
			RAGQuery:    "nutrition dietary guidelines health tips",
		},
	})

	// Patient's actual metrics
	metrics := map[string]float64{
		"steps":           5200,  // Below target
		"fasting_glucose": 108,   // Prediabetes range
		"systolic_bp":     128,   // Elevated
		"diastolic_bp":    82,    // Stage 1 hypertension
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
