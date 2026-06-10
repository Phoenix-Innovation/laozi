package laozi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ---- DSL parser / validator ("test parser") --------------------------------

func TestDSLValidExpressions(t *testing.T) {
	// Every example from the DSL reference must validate cleanly.
	valid := []string{
		`ROUND((SUM(amount) WHERE(type = 'income') OVER(30 days)) / NULLIF(SUM(amount) WHERE(type = 'expense') OVER(30 days), 0) * 100, 2)`,
		`COUNT(*) WHERE(type = 'CREDIT') PERIOD(YTD)`,
		`GINI(amount GROUP_BY(payee))`,
		`CHANGE(amount, 3 months)`,
		`SUM(amount) WHERE(amount > 10000 AND ON(weekends)) OVER(90 days)`,
		`STDEV(amount) WHERE(type = 'expense') OVER(last_year)`,
		`value ^ 2`,
		`income - expenses`,
		`ROUND(ratio, 2)`,
		`ABS(balance)`,
		`SQRT(variance)`,
	}
	for _, e := range valid {
		if errs := CheckDSL(e); len(errs) != 0 {
			t.Errorf("expected VALID, got errors for %q: %v", e, errs)
		}
		if _, err := ParseDSL(e); err != nil {
			t.Errorf("ParseDSL failed for %q: %v", e, err)
		}
	}
}

func TestDSLErrorsAreFlagged(t *testing.T) {
	cases := []struct {
		expr string
		want string // substring expected in at least one error
	}{
		{`SUM(amount`, "expected ')'"},
		{`FOO(x)`, "unknown function FOO"},
		{`ROUND(x)`, "ROUND expects 2"},
		{`SUM('text')`, "numeric"},
		{`WHERE(type = 'income')`, "must follow"},
		{`SUM(amount) OVER(30 lunars)`, "unknown time unit"},
		{`COUNT(*) PERIOD(NEXTYEAR)`, "unknown period"},
		{`GINI(amount)`, "GROUP_BY"},
		{``, "empty"},
		{`1 +`, "unexpected"},
		{`WHERE(type = 'income`, "unterminated string"},
		{`SUM(@)`, "unexpected character"},
		{`COUNT(a, b)`, "COUNT expects exactly 1"},
	}
	for _, c := range cases {
		errs := CheckDSL(c.expr)
		if len(errs) == 0 {
			t.Errorf("expected error for %q, got none", c.expr)
			continue
		}
		found := false
		for _, e := range errs {
			if strings.Contains(e.Msg, c.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: want an error containing %q, got %v", c.expr, c.want, errs)
		}
	}
}

func TestDSLCompilesToSQL(t *testing.T) {
	cases := []struct {
		expr     string
		contains []string
	}{
		{`COUNT(*) WHERE(type = 'CREDIT') PERIOD(YTD)`, []string{"COUNT(", "type = 'CREDIT'", "date_trunc('year'"}},
		{`GINI(amount GROUP_BY(payee))`, []string{"gini(amount)", "GROUP BY payee"}},
		{`SUM(amount) WHERE(type = 'income') OVER(30 days)`, []string{"SUM(", "INTERVAL '30 days'", "type = 'income'"}},
		{`STDEV(amount) WHERE(type = 'expense') OVER(last_year)`, []string{"STDDEV(", "type = 'expense'"}},
	}
	for _, c := range cases {
		sql, err := CompileSQL(c.expr)
		if err != nil {
			t.Errorf("compile %q: %v", c.expr, err)
			continue
		}
		for _, want := range c.contains {
			if !strings.Contains(sql, want) {
				t.Errorf("compile %q: SQL %q missing %q", c.expr, sql, want)
			}
		}
	}
	if _, err := CompileSQL(`FOO(x)`); err == nil {
		t.Error("expected compile error for invalid expression")
	}
}

// ---- Draft / approval workflow ---------------------------------------------

func dslCategory() Category {
	return Category{
		ID:   "revenue",
		Name: "Revenue",
		Thresholds: []Threshold{{
			Metric:     "monthly_revenue",
			Expression: `SUM(amount) WHERE(type = 'revenue') OVER(30 days)`,
			Min:        10000, Max: 50000, Unit: "USD",
			Source: "Benchmark", SourceURL: "https://bench/x",
		}},
	}
}

func TestProposeCreatesDraftNotRegistered(t *testing.T) {
	e := New(WithLLM(mockLLM{resp: `{}`}))
	d, err := e.ProposeCategory(dslCategory())
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if d.Status != StatusDraft {
		t.Errorf("status = %q, want draft", d.Status)
	}
	// Category must NOT be registered yet.
	if _, err := e.AnalyzeCategory(context.Background(), "revenue", map[string]float64{"monthly_revenue": 20000}); err == nil {
		t.Error("category should not be analyzable before approval")
	}
	// Draft carries the compiled SQL for review.
	if len(d.Expressions) != 1 || !d.Expressions[0].Valid || !strings.Contains(d.Expressions[0].SQL, "SUM(") {
		t.Errorf("expected one valid expression review with SQL, got %+v", d.Expressions)
	}
	if len(e.PendingDrafts()) != 1 {
		t.Errorf("expected 1 pending draft, got %d", len(e.PendingDrafts()))
	}
}

func TestApprovePromotesToProduction(t *testing.T) {
	e := New(WithLLM(mockLLM{resp: `{"insight":{"text":"monthly_revenue is within the target band.","severity":"success","reference":"Benchmark - https://bench/x"}}`}))
	d, _ := e.ProposeCategory(dslCategory())
	if err := e.ApproveDraft(d.ID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got, _ := e.Draft(d.ID); got.Status != StatusApproved {
		t.Errorf("status = %q, want approved", got.Status)
	}
	// Now it is analyzable.
	ins, err := e.AnalyzeCategory(context.Background(), "revenue", map[string]float64{"monthly_revenue": 20000})
	if err != nil {
		t.Fatalf("analyze after approval: %v", err)
	}
	if ins.Severity != SeveritySuccess {
		t.Errorf("severity = %q, want success", ins.Severity)
	}
	if len(e.PendingDrafts()) != 0 {
		t.Errorf("expected 0 pending after approval, got %d", len(e.PendingDrafts()))
	}
}

func TestRejectNeverPromotes(t *testing.T) {
	e := New(WithLLM(mockLLM{resp: `{}`}))
	d, _ := e.ProposeCategory(dslCategory())
	if err := e.RejectDraft(d.ID, "thresholds look wrong"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if got, _ := e.Draft(d.ID); got.Status != StatusRejected || got.RejectReason == "" {
		t.Errorf("expected rejected with reason, got %+v", got)
	}
	if _, err := e.AnalyzeCategory(context.Background(), "revenue", map[string]float64{"monthly_revenue": 20000}); err == nil {
		t.Error("rejected category must never be analyzable")
	}
	// Approving a rejected draft must fail.
	if err := e.ApproveDraft(d.ID); err == nil {
		t.Error("approving a rejected draft should fail")
	}
}

func TestProposeRejectsInvalidDSL(t *testing.T) {
	e := New(WithLLM(mockLLM{resp: `{}`}))
	cat := dslCategory()
	cat.Thresholds[0].Expression = `SUM(amount` // syntax error
	d, err := e.ProposeCategory(cat)
	if err == nil {
		t.Fatal("expected error for invalid DSL")
	}
	if d != nil {
		t.Error("no draft should be created for invalid DSL")
	}
	if len(e.PendingDrafts()) != 0 {
		t.Error("invalid proposal must not create a pending draft")
	}
}

type capturingReviewer struct{ got []*Draft }

func (c *capturingReviewer) OnDraft(d *Draft) { c.got = append(c.got, d) }

func TestReviewerHookFires(t *testing.T) {
	rv := &capturingReviewer{}
	e := New(WithLLM(mockLLM{resp: `{}`}), WithReviewer(rv))
	d, _ := e.ProposeCategory(dslCategory())
	if len(rv.got) != 1 || rv.got[0].ID != d.ID {
		t.Errorf("reviewer should be notified once with the new draft, got %+v", rv.got)
	}
}

func TestAutoApproveSkipsGate(t *testing.T) {
	e := New(WithLLM(mockLLM{resp: `{"insight":{"text":"monthly_revenue is within the target band.","severity":"success","reference":"Benchmark - https://bench/x"}}`}), WithConfig(Config{AutoApprove: true}))
	d, err := e.ProposeCategory(dslCategory())
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if d.Status != StatusApproved {
		t.Errorf("AutoApprove should yield approved draft, got %q", d.Status)
	}
	// Registered immediately.
	if _, err := e.AnalyzeCategory(context.Background(), "revenue", map[string]float64{"monthly_revenue": 20000}); err != nil {
		t.Errorf("AutoApprove should register immediately: %v", err)
	}
}

func TestDraftIsJSONSerializable(t *testing.T) {
	// The host app renders the draft, so it must round-trip through JSON.
	e := New(WithLLM(mockLLM{resp: `{}`}))
	d, _ := e.ProposeCategory(dslCategory())
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Draft
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ID != d.ID || back.Status != StatusDraft || len(back.Expressions) != 1 {
		t.Errorf("round-trip mismatch: %+v", back)
	}
}
