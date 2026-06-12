package laozi

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
)

const okLLM = `{"insight":{"text":"current_ratio is within the registered range here.","severity":"success","reference":"CFA Institute - https://cfainstitute.org/ratios"}}`

// C-01: NaN/±Inf metric values must never read as success.
func TestNonFiniteMetricRejected(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		e := New(WithLLM(mockLLM{resp: okLLM}))
		e.AddCategory(Category{ID: "liq", Thresholds: safeTh})
		ins, err := e.AnalyzeCategory(context.Background(), "liq", map[string]float64{"current_ratio": v})
		if err != nil {
			t.Fatalf("v=%v: %v", v, err)
		}
		if ins.Severity != SeverityUnavailable {
			t.Errorf("v=%v: severity=%q, want unavailable", v, ins.Severity)
		}
	}
}

// C-02: a missing REQUIRED metric makes the category unavailable even if another
// metric is present and in range; an Optional metric's absence does not.
func TestPartialMissingRequiredIsUnavailable(t *testing.T) {
	two := []Threshold{
		{Metric: "a", Min: 1, Max: 10, Unit: "u", Source: "S", SourceURL: "https://s/x"},
		{Metric: "b", Min: 1, Max: 10, Unit: "u", Source: "S", SourceURL: "https://s/x"},
	}
	e := New(WithLLM(mockLLM{resp: `{"insight":{"text":"ok","severity":"success","reference":"S - https://s/x"}}`}))
	e.AddCategory(Category{ID: "c", Thresholds: two})
	ins, _ := e.AnalyzeCategory(context.Background(), "c", map[string]float64{"a": 5}) // b missing
	if ins.Severity != SeverityUnavailable {
		t.Errorf("partial-missing required: severity=%q, want unavailable", ins.Severity)
	}

	twoOpt := []Threshold{
		two[0],
		{Metric: "b", Min: 1, Max: 10, Unit: "u", Source: "S", SourceURL: "https://s/x", Optional: true},
	}
	e2 := New(WithLLM(mockLLM{resp: `{"insight":{"text":"a is within the registered range.","severity":"success","reference":"S - https://s/x"}}`}))
	e2.AddCategory(Category{ID: "c2", Thresholds: twoOpt})
	ins2, _ := e2.AnalyzeCategory(context.Background(), "c2", map[string]float64{"a": 5}) // b optional, absent
	if ins2.Severity == SeverityUnavailable {
		t.Error("optional metric absence must not force unavailable")
	}
}

// C-03: registration validates and returns errors.
func TestRegistrationValidation(t *testing.T) {
	e := New(WithLLM(mockLLM{resp: okLLM}))
	bad := map[string]Category{
		"empty ID":     {ID: "", Thresholds: safeTh},
		"min>max":      {ID: "x", Thresholds: []Threshold{{Metric: "m", Min: 9, Max: 1, Unit: "u", Source: "S", SourceURL: "https://s"}}},
		"nan bound":    {ID: "x", Thresholds: []Threshold{{Metric: "m", Min: math.NaN(), Max: 1, Unit: "u", Source: "S", SourceURL: "https://s"}}},
		"empty unit":   {ID: "x", Thresholds: []Threshold{{Metric: "m", Min: 1, Max: 2, Unit: "", Source: "S", SourceURL: "https://s"}}},
		"no source":    {ID: "x", Thresholds: []Threshold{{Metric: "m", Min: 1, Max: 2, Unit: "u"}}},
		"empty metric": {ID: "x", Thresholds: []Threshold{{Metric: "", Min: 1, Max: 2, Unit: "u", Source: "S", SourceURL: "https://s"}}},
	}
	for name, c := range bad {
		if err := e.AddCategory(c); err == nil {
			t.Errorf("%s: expected validation error, got nil", name)
		}
	}
	if err := e.AddCategory(Category{ID: "good", Thresholds: safeTh}); err != nil {
		t.Errorf("valid category rejected: %v", err)
	}
	// re-registering an existing ID must error, not silently overwrite
	if err := e.AddCategory(Category{ID: "good", Thresholds: safeTh}); err == nil {
		t.Error("expected error re-registering an existing category ID")
	}
	if err := e.AddCategories([]Category{{ID: "good", Thresholds: safeTh}}); err == nil {
		t.Error("expected error registering an already-registered ID via AddCategories")
	}
	// duplicate IDs within a batch
	if err := e.AddCategories([]Category{{ID: "d", Thresholds: safeTh}, {ID: "d", Thresholds: safeTh}}); err == nil {
		t.Error("expected duplicate-ID batch error")
	}
}

// C-05: strict audit with no sink fails closed.
func TestStrictAuditRequiresSink(t *testing.T) {
	e := New(WithLLM(mockLLM{resp: okLLM}), WithStrictAudit(true)) // no WithAuditSink
	e.AddCategory(Category{ID: "liq", Thresholds: safeTh})
	if _, err := e.Analyze(context.Background(), map[string]float64{"current_ratio": 2}); err == nil {
		t.Error("strict audit with no sink should fail closed")
	}
}

type flakySink struct {
	mu     sync.Mutex
	n      int
	failOn int
}

func (s *flakySink) Record(context.Context, AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	if s.n == s.failOn {
		return errors.New("sink failure")
	}
	return nil
}

// C-06: under strict audit, a failed audit must not leave committed state.
func TestDraftAtomicityUnderStrictAudit(t *testing.T) {
	// ProposeCategory: audit fails on the first write -> no draft, no category.
	e := New(WithLLM(mockLLM{resp: okLLM}), WithAuditSink(&flakySink{failOn: 1}), WithStrictAudit(true))
	if _, err := e.ProposeCategory(Category{ID: "p", Thresholds: safeTh}, "alice"); err == nil {
		t.Fatal("expected propose to fail under strict audit failure")
	}
	if len(e.PendingDrafts()) != 0 {
		t.Error("failed strict propose must leave no draft")
	}
	if _, err := e.AnalyzeCategory(context.Background(), "p", map[string]float64{"current_ratio": 2}); err == nil {
		t.Error("failed strict propose must not promote a category")
	}

	// ApproveDraft: propose succeeds (sink ok), approve's audit fails -> draft
	// stays pending, category not promoted.
	e2 := New(WithLLM(mockLLM{resp: okLLM}), WithAuditSink(&flakySink{failOn: 2}), WithStrictAudit(true))
	d, err := e2.ProposeCategory(Category{ID: "q", Thresholds: safeTh}, "alice")
	if err != nil {
		t.Fatalf("propose should succeed (sink ok on first call): %v", err)
	}
	if err := e2.ApproveDraft(d.ID, "bob"); err == nil {
		t.Fatal("expected approve to fail under strict audit failure")
	}
	got, _ := e2.Draft(d.ID)
	if got != nil && got.Status == StatusApproved {
		t.Error("failed strict approve must not mark draft approved")
	}
	if _, err := e2.AnalyzeCategory(context.Background(), "q", map[string]float64{"current_ratio": 2}); err == nil {
		t.Error("failed strict approve must not promote the category")
	}

	// RejectDraft: propose succeeds, reject's audit fails -> draft stays pending.
	e3 := New(WithLLM(mockLLM{resp: okLLM}), WithAuditSink(&flakySink{failOn: 2}), WithStrictAudit(true))
	d3, err := e3.ProposeCategory(Category{ID: "r", Thresholds: safeTh}, "alice")
	if err != nil {
		t.Fatalf("propose should succeed (sink ok on first call): %v", err)
	}
	if err := e3.RejectDraft(d3.ID, "bob", "nope"); err == nil {
		t.Fatal("expected reject to fail under strict audit failure")
	}
	if got3, _ := e3.Draft(d3.ID); got3 == nil || got3.Status != StatusDraft {
		t.Error("failed strict reject must leave the draft pending")
	}
}

// C-07: analysis events carry provenance; failures are recorded.
func TestAuditCompletenessAndFailureEvents(t *testing.T) {
	sink := NewMemoryAuditSink()
	e := New(WithLLM(mockLLM{resp: okLLM}), WithAuditSink(sink))
	e.AddCategory(Category{ID: "liq", Version: "v3", Thresholds: safeTh})
	if _, err := e.Analyze(context.Background(), map[string]float64{"current_ratio": 2}); err != nil {
		t.Fatal(err)
	}
	entries := sink.Entries()
	if len(entries) == 0 {
		t.Fatal("no audit entries")
	}
	ev := entries[len(entries)-1]
	if ev.RequestID == "" || ev.Model == "" || ev.PromptVersion == "" || ev.CategoryVersion != "v3" || ev.SourceHash == "" || len(ev.Sources) == 0 {
		t.Errorf("analysis event missing provenance: %+v", ev.AuditEvent)
	}

	// Failure event on LLM error (non-strict so the call returns the error path).
	sink2 := NewMemoryAuditSink()
	e2 := New(WithLLM(mockLLM{err: errors.New("model down")}), WithAuditSink(sink2))
	e2.AddCategory(Category{ID: "liq2", Thresholds: safeTh})
	_, _ = e2.AnalyzeCategory(context.Background(), "liq2", map[string]float64{"current_ratio": 2})
	foundFail := false
	for _, en := range sink2.Entries() {
		if en.Kind == "analysis_failed" && en.ErrorKind != "" {
			foundFail = true
		}
	}
	if !foundFail {
		t.Error("expected an analysis_failed audit event with an error kind")
	}
}

type failRAG struct{}

func (failRAG) Search(context.Context, string, int) ([]RAGResult, error) {
	return nil, errors.New("rag down")
}

// C-09: when RequireRAG is set, a retrieval failure fails closed.
func TestRequireRAGFailsClosed(t *testing.T) {
	sink := NewMemoryAuditSink()
	e := New(WithLLM(mockLLM{resp: okLLM}), WithRAG(failRAG{}), WithAuditSink(sink))
	e.AddCategory(Category{ID: "liq", Thresholds: safeTh, RAGQuery: "liquidity", RequireRAG: true})
	ins, err := e.AnalyzeCategory(context.Background(), "liq", map[string]float64{"current_ratio": 2})
	if err != nil {
		t.Fatal(err)
	}
	if ins.Severity != SeverityUnavailable {
		t.Errorf("required-RAG failure: severity=%q, want unavailable", ins.Severity)
	}
	hasRagViolation := false
	for _, v := range ins.Violations {
		if v.Kind == "rag_unavailable" {
			hasRagViolation = true
		}
	}
	if !hasRagViolation {
		t.Error("expected a rag_unavailable violation")
	}
	// C-07: the retrieval failure must be recorded in the audit trail.
	foundRetrievalFailed := false
	for _, en := range sink.Entries() {
		if en.Kind == "retrieval_failed" {
			foundRetrievalFailed = true
		}
	}
	if !foundRetrievalFailed {
		t.Error("expected a retrieval_failed audit event")
	}
}

// C-08: getters return deep copies — mutating a returned value must not change
// engine/audit/classifier state.
func TestGettersReturnCopies(t *testing.T) {
	// Draft copy
	e := New(WithLLM(mockLLM{resp: okLLM}))
	d, err := e.ProposeCategory(Category{ID: "p", Name: "orig", Thresholds: safeTh}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := e.Draft(d.ID)
	got.Status = StatusApproved
	got.Category.Name = "hacked"
	if got.Category.Thresholds != nil {
		got.Category.Thresholds[0].Min = -999
	}
	again, _ := e.Draft(d.ID)
	if again.Status == StatusApproved || again.Category.Name == "hacked" || again.Category.Thresholds[0].Min == -999 {
		t.Error("mutating a returned Draft changed engine state")
	}

	// Audit entry copy
	sink := NewMemoryAuditSink()
	e2 := New(WithLLM(mockLLM{resp: okLLM}), WithAuditSink(sink))
	e2.AddCategory(Category{ID: "liq", Thresholds: safeTh})
	if _, err := e2.Analyze(context.Background(), map[string]float64{"current_ratio": 2}); err != nil {
		t.Fatal(err)
	}
	ents := sink.Entries()
	if len(ents) > 0 && ents[0].Insight != nil {
		ents[0].Insight.Text = "tampered"
		if len(ents[0].Sources) > 0 {
			ents[0].Sources[0] = "tampered"
		}
	}
	if !sink.Verify() {
		t.Error("mutating a returned audit entry broke the stored chain")
	}
	ents2 := sink.Entries()
	if ents2[0].Insight != nil && ents2[0].Insight.Text == "tampered" {
		t.Error("mutating a returned audit entry changed stored state")
	}

	// Domain copy
	clf := NewClassifier(WithDomains([]Domain{{Name: "cardiac", Keywords: []string{"k"}, Categories: []string{"c"}}}))
	dom, _ := clf.Domain("cardiac")
	dom.Keywords[0] = "hacked"
	dom.Categories[0] = "hacked"
	again2, _ := clf.Domain("cardiac")
	if again2.Keywords[0] == "hacked" || again2.Categories[0] == "hacked" {
		t.Error("mutating a returned Domain changed classifier state")
	}
}

// C-04: numeric traceability is exact at display precision — the old ~0.5%
// tolerance window (which widened with magnitude) is gone.
func TestNumberTraceabilityIsExactAtDisplay(t *testing.T) {
	cat := Category{ID: "c", Thresholds: []Threshold{
		{Metric: "r", Min: 900, Max: 1100, Unit: "u", Source: "S", SourceURL: "https://s/x"},
	}}
	c := computeAnalysis(cat, map[string]float64{"r": 1000})
	if !c.isAllowed(1000) {
		t.Error("the exact computed value must be traceable")
	}
	// Under the old tol = max(0.01, 0.005*|a|) = 5.0 at a=1000, a fabricated
	// 1004 was "close enough". At display precision it is not.
	if c.isAllowed(1004) {
		t.Error("1004 must not be traceable to 1000 (old 0.5% window closed)")
	}
}

// C-10: compiled SQL escapes literals/identifiers, and unary/exponent
// precedence is well-defined and locked.
func TestDSLCompileSafetyAndPrecedence(t *testing.T) {
	if got := sqlEscapeLiteral("O'Brien"); got != "O''Brien" {
		t.Errorf("single quote not doubled: %q", got)
	}
	if got := sqlSafeIdent("good_col"); got != "good_col" {
		t.Errorf("safe identifier changed: %q", got)
	}
	if got := sqlSafeIdent(`b"ad name`); got != `"b""ad name"` {
		t.Errorf("unsafe identifier not quoted/escaped: %q", got)
	}
	if !safePgIdentForTest("public.laozi_audit") || safePgIdentForTest("t; DROP TABLE x") {
		t.Error("pg table-name validation wrong")
	}
	// -value ^ 2 parses as (-value)^2 (documented precedence).
	sql, err := CompileSQL(`-value ^ 2`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "power((-value), 2)") {
		t.Errorf("precedence -a^b: got %q", sql)
	}
	// a - -b: unary minus inside subtraction.
	sql2, err := CompileSQL(`amount - -bonus`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql2, "(-bonus)") {
		t.Errorf("a - -b: got %q", sql2)
	}
}

// mirror of postgres.safePgIdent for a core-package unit check of the rule.
func safePgIdentForTest(name string) bool {
	if name == "" {
		return false
	}
	for _, part := range strings.Split(name, ".") {
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			c := part[i]
			ok := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
			if i > 0 {
				ok = ok || (c >= '0' && c <= '9')
			}
			if !ok {
				return false
			}
		}
	}
	return true
}
