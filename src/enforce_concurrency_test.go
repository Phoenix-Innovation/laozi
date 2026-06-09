package laozi

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// compliantLLM is a well-behaved mock: it reads the EXPECTED SEVERITY and a
// source URL out of the prompt the engine built, and echoes them back. This
// lets the all-categories happy path assert ZERO violations.
type compliantLLM struct{}

func (compliantLLM) Chat(_ context.Context, _, user string) (string, error) {
	sev := "success"
	if i := strings.Index(user, "EXPECTED SEVERITY: "); i >= 0 {
		sev = strings.Fields(user[i+len("EXPECTED SEVERITY: "):])[0]
	} else if strings.Contains(user, "Educational") {
		sev = "info"
	}
	ref := "Source - https://none"
	if i := strings.Index(user, "URL: "); i >= 0 {
		ref = "Source - " + strings.Fields(user[i+len("URL: "):])[0]
	}
	// Deliberately number-free text so the prose check stays clean.
	return fmt.Sprintf(`{"insight":{"text":"Reviewed against the provided guideline.","severity":%q,"reference":%q}}`, sev, ref), nil
}

func liquidityCats() []Category {
	return []Category{
		{ID: "liquidity", Name: "Liquidity", Thresholds: []Threshold{
			{Metric: "current_ratio", Min: 1.5, Max: 3.0, Unit: "ratio", Source: "CFA", SourceURL: "https://cfa/liq"}}},
		{ID: "profitability", Name: "Profitability", Thresholds: []Threshold{
			{Metric: "margin", Min: 10, Max: 40, Unit: "%", Source: "McKinsey", SourceURL: "https://mck/margin"}}},
		{ID: "leverage", Name: "Leverage", Thresholds: []Threshold{
			{Metric: "debt_ratio", Min: 0, Max: 0.5, Unit: "ratio", Source: "S&P", SourceURL: "https://sp/lev"}}},
		{ID: "edu", Name: "Education", Educational: true},
	}
}

var allMetrics = map[string]float64{"current_ratio": 1.2, "margin": 25, "debt_ratio": 0.8}

// indexByCategory turns Analyze's order-independent slice into a lookup.
func indexByCategory(ins []Insight) map[string]Insight {
	m := make(map[string]Insight, len(ins))
	for _, i := range ins {
		m[i.CategoryID] = i
	}
	return m
}

// TestAnalyzeAllCategories drives the all-categories Analyze path. The expected
// severities form the truth table; result order is randomized by Go's map
// iteration, so we assert by CategoryID, not position.
func TestAnalyzeAllCategories(t *testing.T) {
	want := map[string]Severity{
		"liquidity":     SeverityWarning, // 1.2 < 1.5
		"profitability": SeveritySuccess, // 10 <= 25 <= 40
		"leverage":      SeverityWarning, // 0.8 > 0.5
		"edu":           SeverityInfo,    // educational
	}

	e := New(WithLLM(compliantLLM{}))
	e.AddCategories(liquidityCats())

	ins, err := e.Analyze(context.Background(), allMetrics)
	if err != nil {
		t.Fatal(err)
	}
	if len(ins) != len(want) {
		t.Fatalf("got %d insights, want %d", len(ins), len(want))
	}
	got := indexByCategory(ins)
	for id, sev := range want {
		in, ok := got[id]
		if !ok {
			t.Errorf("missing insight for category %q", id)
			continue
		}
		if in.Severity != sev {
			t.Errorf("%s: severity = %q, want %q", id, in.Severity, sev)
		}
		if len(in.Violations) != 0 {
			t.Errorf("%s: compliant LLM should yield no violations, got %+v", id, in.Violations)
		}
	}
}

// TestAnalyzeEnforcesEveryCategory proves enforcement is applied to every item
// in the batch, not just the first: a uniformly lying LLM still gets each
// category's severity corrected independently.
func TestAnalyzeEnforcesEveryCategory(t *testing.T) {
	liar := mockLLM{resp: `{"insight":{"text":"All good.","severity":"success","reference":"X - https://evil.example"}}`}
	e := New(WithLLM(liar))
	e.AddCategories(liquidityCats())

	ins, err := e.Analyze(context.Background(), allMetrics)
	if err != nil {
		t.Fatal(err)
	}
	got := indexByCategory(ins)
	for _, id := range []string{"liquidity", "leverage"} { // both out of range
		if got[id].Severity != SeverityWarning {
			t.Errorf("%s: severity not corrected, got %q", id, got[id].Severity)
		}
		if !hasViolation(got[id].Violations, "severity") {
			t.Errorf("%s: expected severity violation logged", id)
		}
		if strings.Contains(got[id].Reference, "evil.example") {
			t.Errorf("%s: bogus citation survived: %q", id, got[id].Reference)
		}
	}
}

// TestAnalyzeParallelRace hammers Analyze from many goroutines while context is
// being mutated (the adaptive-context pattern). Run with -race. It asserts both
// the absence of races and that every concurrent call still returns correct,
// fully-enforced results.
func TestAnalyzeParallelRace(t *testing.T) {
	e := New(WithLLM(compliantLLM{}))
	e.AddCategories(liquidityCats())

	var wg sync.WaitGroup
	errs := make(chan string, 256)
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				ins, err := e.Analyze(context.Background(), allMetrics)
				if err != nil {
					errs <- fmt.Sprintf("g%d: %v", g, err)
					return
				}
				m := indexByCategory(ins)
				if m["liquidity"].Severity != SeverityWarning || m["profitability"].Severity != SeveritySuccess {
					errs <- fmt.Sprintf("g%d: wrong severities under concurrency", g)
					return
				}
				e.SetContext("turn", j) // concurrent writer
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}
