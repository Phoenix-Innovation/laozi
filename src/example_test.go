package laozi_test

import (
	"context"
	"fmt"

	"github.com/Phoenix-Innovation/laozi"
)

// echoLLM is a trivial LLM stub that returns a fixed JSON response.
// In production, replace this with a real OpenAI / Azure / local-model client
// that implements laozi.LLMClient.
type echoLLM struct{}

func (echoLLM) Chat(_ context.Context, _, _ string) (string, error) {
	return `{"insight":{
		"text":"Fasting glucose is 112.00 mg/dL, above the normal maximum of 99 mg/dL. This falls in the prediabetes range — consider dietary changes and retesting in 3 months.",
		"severity":"success",
		"reference":"Made-Up Journal - https://fake.org",
		"suggested_goal":{"metric":"fasting_glucose","target":80,"unit":"mg/dL","comparison":"lte","description":"aim lower"}
	}}`, nil
}

func Example() {
	// 1. Create the engine with a mock LLM (swap for real client in production)
	engine := laozi.New(
		laozi.WithLLM(echoLLM{}),
		// laozi.WithRAG(myRAGStore),  // optional — plug in a vector DB
		// laozi.WithStrict(true),      // optional — replace LLM prose on number violations
	)

	// 2. Register a category with clinical thresholds
	engine.AddCategory(laozi.Category{
		ID:   "glucose",
		Name: "Blood Glucose",
		Thresholds: []laozi.Threshold{{
			Metric:    "fasting_glucose",
			Min:       70,
			Max:       99,
			OptimalMin: 70,
			OptimalMax: 90,
			Unit:      "mg/dL",
			Source:    "American Diabetes Association",
			SourceURL: "https://diabetes.org/diagnosis",
		}},
	})

	// 3. Analyze — the enforcement layer runs automatically
	insights, err := engine.Analyze(context.Background(), map[string]float64{
		"fasting_glucose": 112.0, // above max of 99 → warning
	})
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	// 4. Inspect the result — enforcement has corrected the LLM's mistakes
	for _, ins := range insights {
		fmt.Println("Severity:", ins.Severity)
		fmt.Println("Reference contains diabetes.org:", contains(ins.Reference, "diabetes.org"))
		fmt.Println("Violations:")
		for _, v := range ins.Violations {
			fmt.Printf("  [%s] LLM said %q → enforced %q\n", v.Kind, v.LLMValue, v.Enforced)
		}
		if ins.SuggestedGoal != nil {
			fmt.Printf("Goal: %s %s %.0f %s\n",
				ins.SuggestedGoal.Metric, ins.SuggestedGoal.Comparison,
				ins.SuggestedGoal.Target, ins.SuggestedGoal.Unit)
		}
	}

	// Output:
	// Severity: warning
	// Reference contains diabetes.org: true
	// Violations:
	//   [severity] LLM said "success" → enforced "warning"
	//   [reference] LLM said "Made-Up Journal - https://fake.org" → enforced "American Diabetes Association - https://diabetes.org/diagnosis"
	//   [goal] LLM said "target=80.00 unit=mg/dL cmp=lte" → enforced "target=99.00 unit=mg/dL cmp=lte"
	//   [number] LLM said "3" → enforced ""
	// Goal: fasting_glucose lte 99 mg/dL
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
