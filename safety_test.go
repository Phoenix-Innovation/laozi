package laozi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

var safeTh = []Threshold{{
	Metric: "current_ratio", Min: 1.5, Max: 3.0, Unit: "ratio",
	Source: "CFA Institute", SourceURL: "https://cfainstitute.org/ratios",
}}

// Bug 1 + 7: a category whose required metric is absent must NOT read as
// success, and its text must not be empty.
func TestMissingMetricIsUnavailableNotSuccess(t *testing.T) {
	e := New(WithLLM(mockLLM{resp: `{"insight":{"text":"x","severity":"success","reference":"CFA - https://cfainstitute.org/ratios"}}`}))
	e.AddCategory(Category{ID: "liquidity", Name: "Liquidity", Thresholds: safeTh})

	ins, err := e.AnalyzeCategory(context.Background(), "liquidity", map[string]float64{"unrelated": 5})
	if err != nil {
		t.Fatal(err)
	}
	if ins.Severity == SeveritySuccess {
		t.Fatal("missing required metric must never be reported as success")
	}
	if ins.Severity != SeverityUnavailable {
		t.Errorf("severity = %q, want unavailable", ins.Severity)
	}
	if strings.TrimSpace(ins.Text) == "" {
		t.Error("unavailable insight must have non-empty text")
	}
	if len(ins.Text) < MinInsightTextLen {
		t.Errorf("unavailable text too short: %d", len(ins.Text))
	}
}

// Bug 7: strict fallback (LLM error) with missing metrics must still produce
// explicit, non-empty prose — not an empty success.
func TestStrictFallbackNeverEmptyText(t *testing.T) {
	e := New(
		WithLLM(mockLLM{err: errors.New("model down")}),
		WithStrict(true),
	)
	e.AddCategory(Category{ID: "liquidity", Name: "Liquidity", Thresholds: safeTh})

	ins, err := e.AnalyzeCategory(context.Background(), "liquidity", map[string]float64{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(ins.Text) == "" {
		t.Fatal("strict fallback returned empty text for absent metrics")
	}
	if ins.Severity == SeveritySuccess {
		t.Error("strict fallback for absent metrics must not be success")
	}
}

// Bug 2: registering categories concurrently with Analyze must not race.
func TestConcurrentRegistryMutationAndAnalyze(t *testing.T) {
	e := New(WithLLM(mockLLM{resp: `{"insight":{"text":"current_ratio within the registered range.","severity":"success","reference":"CFA - https://cfainstitute.org/ratios"}}`}))
	e.AddCategory(Category{ID: "seed", Name: "Seed", Thresholds: safeTh})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			e.AddCategory(Category{ID: string(rune('a' + n%26)), Thresholds: safeTh})
		}(i)
		go func() {
			defer wg.Done()
			_, _ = e.Analyze(context.Background(), map[string]float64{"current_ratio": 2.0})
		}()
	}
	wg.Wait()
}

// Bug 6: insight order must be deterministic across runs.
func TestDeterministicOrdering(t *testing.T) {
	e := New(WithLLM(mockLLM{resp: `{"insight":{"text":"current_ratio within the registered range.","severity":"success","reference":"CFA - https://cfainstitute.org/ratios"}}`}))
	for _, id := range []string{"zebra", "alpha", "mango", "beta"} {
		e.AddCategory(Category{ID: id, Thresholds: safeTh})
	}
	first, _ := e.Analyze(context.Background(), map[string]float64{"current_ratio": 2.0})
	for run := 0; run < 5; run++ {
		got, _ := e.Analyze(context.Background(), map[string]float64{"current_ratio": 2.0})
		if len(got) != len(first) {
			t.Fatalf("len mismatch: %d vs %d", len(got), len(first))
		}
		for i := range got {
			if got[i].CategoryID != first[i].CategoryID {
				t.Fatalf("order not stable at %d: %q vs %q", i, got[i].CategoryID, first[i].CategoryID)
			}
		}
	}
	// And it should actually be sorted by ID.
	for i := 1; i < len(first); i++ {
		if first[i-1].CategoryID > first[i].CategoryID {
			t.Errorf("not sorted: %q before %q", first[i-1].CategoryID, first[i].CategoryID)
		}
	}
}

// Bug 4: layer-1 classification must require the response to BE one category
// word, not merely contain one.
func TestClassifierRequiresExactWord(t *testing.T) {
	domains := []Domain{
		{Name: "cardiac", Keywords: []string{"zzzznevermatch"}},
		{Name: "metabolic", Keywords: []string{"zzzznevermatch"}},
	}
	// Adversarial/commentary output that merely contains a domain name.
	chatty := NewClassifier(WithDomains(domains), WithClassifierLLM(mockLLM{resp: "I think cardiac, but possibly metabolic too."}))
	got := chatty.Classify(context.Background(), "irrelevant", nil)
	if got.Layer == 1 {
		t.Errorf("substring/commentary output must not be accepted at layer 1, got domain %q", got.Domain)
	}
	// Exact word is accepted.
	exact := NewClassifier(WithDomains(domains), WithClassifierLLM(mockLLM{resp: "cardiac\n"}))
	got = exact.Classify(context.Background(), "irrelevant", nil)
	if !(got.Layer == 1 && got.Domain == "cardiac") {
		t.Errorf("exact word should classify at layer 1 as cardiac, got %+v", got)
	}
}

// Bug 3: WithStrictAudit makes a failed audit write fail the operation;
// the default swallows it.
type failingSink struct{}

func (failingSink) Record(context.Context, AuditEvent) error { return errors.New("sink down") }

func TestStrictAuditFailsClosed(t *testing.T) {
	mk := func(strict bool) *Engine {
		opts := []Option{
			WithLLM(mockLLM{resp: `{"insight":{"text":"current_ratio within the registered range.","severity":"success","reference":"CFA - https://cfainstitute.org/ratios"}}`}),
			WithAuditSink(failingSink{}),
		}
		if strict {
			opts = append(opts, WithStrictAudit(true))
		}
		e := New(opts...)
		e.AddCategory(Category{ID: "liquidity", Thresholds: safeTh})
		return e
	}
	// Default: audit failure is swallowed.
	if _, err := mk(false).Analyze(context.Background(), map[string]float64{"current_ratio": 2.0}); err != nil {
		t.Errorf("default should not fail on audit error, got %v", err)
	}
	// Strict: audit failure fails the operation.
	if _, err := mk(true).Analyze(context.Background(), map[string]float64{"current_ratio": 2.0}); err == nil {
		t.Error("WithStrictAudit should fail the analysis when the sink errors")
	}
}
