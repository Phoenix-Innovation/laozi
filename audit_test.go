package laozi

import (
	"context"
	"testing"
)

// recordingSink captures events for assertions.
type recordingSink struct{ events []AuditEvent }

func (s *recordingSink) Record(_ context.Context, e AuditEvent) error {
	s.events = append(s.events, e)
	return nil
}

func (s *recordingSink) ofKind(kind string) []AuditEvent {
	var out []AuditEvent
	for _, e := range s.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func TestAuditRecordsAnalysis(t *testing.T) {
	sink := &recordingSink{}
	e := New(
		WithLLM(mockLLM{resp: `{"insight":{"text":"current_ratio is below the registered minimum range.","severity":"success","reference":"CFA - https://cfa/x"}}`}),
		WithAuditSink(sink),
	)
	e.AddCategory(Category{ID: "liquidity", Name: "Liquidity",
		Thresholds: []Threshold{{Metric: "current_ratio", Min: 1.5, Max: 3, Unit: "ratio", Source: "CFA", SourceURL: "https://cfa/x"}}})

	if _, err := e.Analyze(context.Background(), map[string]float64{"current_ratio": 1.2}); err != nil {
		t.Fatal(err)
	}
	an := sink.ofKind("analysis")
	if len(an) != 1 {
		t.Fatalf("expected 1 analysis event, got %d", len(an))
	}
	ev := an[0]
	if ev.CategoryID != "liquidity" || ev.Insight == nil {
		t.Fatalf("analysis event missing fields: %+v", ev)
	}
	// The enforced severity and the violations (the proof) are captured.
	if ev.Insight.Severity != SeverityWarning {
		t.Errorf("expected enforced severity in audit, got %q", ev.Insight.Severity)
	}
	if len(ev.Insight.Violations) == 0 {
		t.Error("expected violations recorded in audit event")
	}
	if ev.Time.IsZero() {
		t.Error("audit event missing timestamp")
	}
}

func TestAuditRecordsHumanLoopWithWhoAndWhen(t *testing.T) {
	sink := &recordingSink{}
	e := New(WithLLM(mockLLM{resp: `{}`}), WithAuditSink(sink))

	d, err := e.ProposeCategory(dslCategory(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if d.CreatedBy != "alice" || d.CreatedAt.IsZero() {
		t.Errorf("draft should record proposer who/when: %+v", d)
	}
	if err := e.ApproveDraft(d.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Draft(d.ID)
	if got.DecidedBy != "bob" || got.DecidedAt.IsZero() {
		t.Errorf("draft should record approver who/when: %+v", got)
	}

	// Audit stream carries both events with actors.
	prop := sink.ofKind("draft_proposed")
	appr := sink.ofKind("draft_approved")
	if len(prop) != 1 || prop[0].Actor != "alice" || prop[0].DraftID != d.ID {
		t.Errorf("proposed event wrong: %+v", prop)
	}
	if len(appr) != 1 || appr[0].Actor != "bob" || appr[0].DraftID != d.ID {
		t.Errorf("approved event wrong: %+v", appr)
	}
}

func TestAuditRecordsRejection(t *testing.T) {
	sink := &recordingSink{}
	e := New(WithLLM(mockLLM{resp: `{}`}), WithAuditSink(sink))
	d, _ := e.ProposeCategory(dslCategory(), "alice")
	if err := e.RejectDraft(d.ID, "carol", "thresholds wrong"); err != nil {
		t.Fatal(err)
	}
	rej := sink.ofKind("draft_rejected")
	if len(rej) != 1 || rej[0].Actor != "carol" || rej[0].Detail != "thresholds wrong" {
		t.Errorf("rejection event wrong: %+v", rej)
	}
}

func TestMemoryAuditSinkHashChain(t *testing.T) {
	m := NewMemoryAuditSink()
	for i := 0; i < 4; i++ {
		_ = m.Record(context.Background(), AuditEvent{Kind: "analysis", CategoryID: "c"})
	}
	entries := m.Entries()
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}
	// Each entry links to the previous entry's hash.
	if entries[0].PrevHash != "" {
		t.Error("first entry should have empty PrevHash")
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].PrevHash != entries[i-1].Hash {
			t.Errorf("entry %d not linked to previous hash", i)
		}
	}
	if !m.Verify() {
		t.Error("intact chain should verify")
	}
	// Tamper with a past entry → chain must fail verification.
	m.entries[1].Actor = "intruder"
	if m.Verify() {
		t.Error("tampered chain should NOT verify")
	}
}

func TestNoAuditSinkIsNoop(t *testing.T) {
	// No WithAuditSink → analysis and drafts still work, no panic.
	e := New(WithLLM(mockLLM{resp: `{"insight":{"text":"current_ratio is within the registered range.","severity":"success","reference":"CFA - https://cfa/x"}}`}))
	e.AddCategory(Category{ID: "liquidity", Name: "Liquidity",
		Thresholds: []Threshold{{Metric: "current_ratio", Min: 1.5, Max: 3, Unit: "ratio", Source: "CFA", SourceURL: "https://cfa/x"}}})
	if _, err := e.Analyze(context.Background(), map[string]float64{"current_ratio": 2.0}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ProposeCategory(dslCategory(), "alice"); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryAuditSinkConcurrent(t *testing.T) {
	// Parallel Analyze emits analysis events from multiple goroutines; the
	// reference sink must be safe under -race.
	sink := NewMemoryAuditSink()
	e := New(
		WithLLM(mockLLM{resp: `{"insight":{"text":"reading is within the registered range here.","severity":"success","reference":"CFA - https://cfa/x"}}`}),
		WithAuditSink(sink),
		WithConfig(Config{MaxParallel: 8}),
	)
	for i := 0; i < 6; i++ {
		e.AddCategory(Category{ID: string(rune('a' + i)), Name: "C",
			Thresholds: []Threshold{{Metric: "m", Min: 0, Max: 100, Unit: "u", Source: "CFA", SourceURL: "https://cfa/x"}}})
	}
	if _, err := e.Analyze(context.Background(), map[string]float64{"m": 50}); err != nil {
		t.Fatal(err)
	}
	if len(sink.Entries()) != 6 {
		t.Errorf("expected 6 audit entries, got %d", len(sink.Entries()))
	}
	if !sink.Verify() {
		t.Error("concurrent-written chain should verify")
	}
}
