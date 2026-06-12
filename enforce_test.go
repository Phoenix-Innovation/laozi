package laozi

import (
	"context"
	"strings"
	"testing"
)

type mockLLM struct {
	resp string
	err  error
}

func (m mockLLM) Chat(_ context.Context, _, _ string) (string, error) { return m.resp, m.err }

// f returns a pointer to a float literal (for optional expected values).
func f(v float64) *float64 { return &v }

var ratioTh = []Threshold{{
	Metric: "current_ratio", Min: 1.5, Max: 3.0, Unit: "ratio",
	Source: "CFA Institute", SourceURL: "https://cfainstitute.org/ratios",
}}

type row struct {
	name        string
	educational bool
	thresholds  []Threshold
	metrics     map[string]float64
	strict      bool
	llmResp     string
	llmErr      error

	wantErr        bool
	wantSeverity   Severity
	refContains    string
	refOmits       string
	goalDropped    bool     // SuggestedGoal neutralized to empty metric
	goalTarget     *float64 // expected corrected target
	textOmits      string   // substring that must NOT survive
	wantViolations []string // kinds that must be present
}

func TestTruthTable(t *testing.T) {
	good := `"reference":"CFA Institute - https://cfainstitute.org/ratios"`
	rows := []row{
		{name: "01_within_honest", thresholds: ratioTh, metrics: map[string]float64{"current_ratio": 2.0}, strict: false,
			llmResp:      `{"insight":{"text":"current_ratio is 2.00, within the healthy range of 1.50 to 3.00.","severity":"success",` + good + `}}`,
			wantSeverity: SeveritySuccess, refContains: "cfainstitute.org", wantViolations: []string{}},

		{name: "02_below_honest", thresholds: ratioTh, metrics: map[string]float64{"current_ratio": 1.2},
			llmResp:      `{"insight":{"text":"current_ratio is 1.20, below the minimum of 1.50.","severity":"warning",` + good + `}}`,
			wantSeverity: SeverityWarning, refContains: "cfainstitute.org", wantViolations: []string{}},

		{name: "03_above_honest", thresholds: ratioTh, metrics: map[string]float64{"current_ratio": 4.0},
			llmResp:      `{"insight":{"text":"current_ratio is 4.00, above the maximum of 3.00.","severity":"warning",` + good + `}}`,
			wantSeverity: SeverityWarning, refContains: "cfainstitute.org", wantViolations: []string{}},

		{name: "04_severity_lie", thresholds: ratioTh, metrics: map[string]float64{"current_ratio": 1.2},
			llmResp:      `{"insight":{"text":"current_ratio is 1.20.","severity":"success",` + good + `}}`,
			wantSeverity: SeverityWarning, wantViolations: []string{"severity"}},

		{name: "05_educational", educational: true, thresholds: nil, metrics: map[string]float64{},
			llmResp:      `{"insight":{"text":"Did you know liquidity buffers reduce default risk?","severity":"success","reference":"X - https://x"}}`,
			wantSeverity: SeverityInfo, wantViolations: []string{"severity"}},

		{name: "06_missing_metric", thresholds: ratioTh, metrics: map[string]float64{"unrelated": 5},
			llmResp:      `{"insight":{"text":"No current_ratio data was provided.","severity":"success",` + good + `}}`,
			wantSeverity: SeverityUnavailable, refContains: "cfainstitute.org", wantViolations: []string{"unavailable"}},

		{name: "07_bogus_citation", thresholds: ratioTh, metrics: map[string]float64{"current_ratio": 1.2},
			llmResp:      `{"insight":{"text":"current_ratio is 1.20.","severity":"warning","reference":"Made Up - https://evil.example/fake"}}`,
			wantSeverity: SeverityWarning, refContains: "cfainstitute.org", refOmits: "evil.example", wantViolations: []string{"reference"}},

		{name: "08_goal_unknown_metric", thresholds: ratioTh, metrics: map[string]float64{"current_ratio": 1.2},
			llmResp:      `{"insight":{"text":"current_ratio is 1.20.","severity":"warning",` + good + `,"suggested_goal":{"metric":"nonexistent","target":5,"unit":"x","comparison":"gte"}}}`,
			wantSeverity: SeverityWarning, goalDropped: true, wantViolations: []string{"goal"}},

		{name: "09_goal_wrong_target", thresholds: ratioTh, metrics: map[string]float64{"current_ratio": 1.2},
			llmResp:      `{"insight":{"text":"current_ratio is 1.20.","severity":"warning",` + good + `,"suggested_goal":{"metric":"current_ratio","target":99,"unit":"x","comparison":"gte"}}}`,
			wantSeverity: SeverityWarning, goalTarget: f(1.5), wantViolations: []string{"goal"}},

		{name: "10_invented_number_strict", thresholds: ratioTh, metrics: map[string]float64{"current_ratio": 1.2}, strict: true,
			llmResp:      `{"insight":{"text":"current_ratio of 999 looks great.","severity":"warning",` + good + `}}`,
			wantSeverity: SeverityWarning, textOmits: "999", wantViolations: []string{"number"}},

		{name: "11_invented_number_lax", thresholds: ratioTh, metrics: map[string]float64{"current_ratio": 1.2}, strict: false,
			llmResp:      `{"insight":{"text":"current_ratio of 999 looks great.","severity":"warning",` + good + `}}`,
			wantSeverity: SeverityWarning, wantViolations: []string{"number"}},

		{name: "12_parsefail_strict", thresholds: ratioTh, metrics: map[string]float64{"current_ratio": 1.2}, strict: true,
			llmResp:      `not json at all`,
			wantSeverity: SeverityWarning, refContains: "cfainstitute.org", textOmits: "not json", wantViolations: []string{"parse"}},

		{name: "13_parsefail_lax", thresholds: ratioTh, metrics: map[string]float64{"current_ratio": 1.2}, strict: false,
			llmResp: `not json at all`, wantErr: true},

		{name: "14_multi_one_bad", thresholds: []Threshold{
			{Metric: "current_ratio", Min: 1.5, Max: 3.0, Unit: "ratio", Source: "CFA", SourceURL: "https://cfa/x"},
			{Metric: "quick_ratio", Min: 1.0, Max: 2.0, Unit: "ratio", Source: "CFA", SourceURL: "https://cfa/x"},
		}, metrics: map[string]float64{"current_ratio": 2.0, "quick_ratio": 0.5},
			llmResp:      `{"insight":{"text":"Ratios reviewed against the stated guidelines.","severity":"success","reference":"CFA - https://cfa/x"}}`,
			wantSeverity: SeverityWarning, wantViolations: []string{"severity"}},
	}

	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			e := New(WithLLM(mockLLM{resp: r.llmResp, err: r.llmErr}), WithStrict(r.strict))
			cat := Category{ID: "c", Name: "C", Educational: r.educational, Thresholds: r.thresholds}
			e.AddCategory(cat)

			ins, err := e.AnalyzeCategory(context.Background(), "c", r.metrics)
			if r.wantErr {
				if err == nil {
					t.Fatalf("expected error, got insight %+v", ins)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ins.Severity != r.wantSeverity {
				t.Errorf("severity = %q, want %q", ins.Severity, r.wantSeverity)
			}
			if r.refContains != "" && !strings.Contains(ins.Reference, r.refContains) {
				t.Errorf("reference %q missing %q", ins.Reference, r.refContains)
			}
			if r.refOmits != "" && strings.Contains(ins.Reference, r.refOmits) {
				t.Errorf("reference %q should not contain %q", ins.Reference, r.refOmits)
			}
			if r.goalDropped && (ins.SuggestedGoal == nil || ins.SuggestedGoal.Metric != "") {
				t.Errorf("goal should be dropped, got %+v", ins.SuggestedGoal)
			}
			if r.goalTarget != nil && (ins.SuggestedGoal == nil || ins.SuggestedGoal.Target != *r.goalTarget) {
				t.Errorf("goal target = %+v, want %v", ins.SuggestedGoal, *r.goalTarget)
			}
			if r.textOmits != "" && strings.Contains(ins.Text, r.textOmits) {
				t.Errorf("text %q should not contain %q", ins.Text, r.textOmits)
			}
			for _, k := range r.wantViolations {
				if !hasViolation(ins.Violations, k) {
					t.Errorf("missing violation kind %q; got %+v", k, ins.Violations)
				}
			}
			if len(r.wantViolations) == 0 && len(ins.Violations) != 0 {
				t.Errorf("expected no violations, got %+v", ins.Violations)
			}
		})
	}
}

func hasViolation(vs []Violation, kind string) bool {
	for _, v := range vs {
		if v.Kind == kind {
			return true
		}
	}
	return false
}
