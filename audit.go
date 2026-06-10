package laozi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// ============================================================================
// Durable audit seam
//
// Laozi computes the auditable facts — the enforced severity/citation/number
// corrections (Insight.Violations) and the human draft decisions (who/when) —
// but it does NOT pick a datastore. Persistence is implementation-dependent:
// hosts plug an AuditSink over Postgres, an append-only log, an object store,
// Kafka, a WORM bucket, etc. This mirrors how RAGStore lets hosts plug their
// own retrieval. Without a sink, auditing is a no-op (records still live on the
// returned Insight / Draft for the life of the call).
// ============================================================================

// AuditEvent is one record. Kind discriminates analysis/enforcement events from
// human draft-lifecycle events. Actor/Time answer "who" and "when" for the
// human loop; analysis events leave Actor empty (the engine, not a person).
type AuditEvent struct {
	Time       time.Time          `json:"time"`
	Kind       string             `json:"kind"` // analysis | draft_proposed | draft_approved | draft_rejected
	Actor      string             `json:"actor,omitempty"`
	CategoryID string             `json:"category_id,omitempty"`
	Metrics    map[string]float64 `json:"metrics,omitempty"`
	Insight    *Insight           `json:"insight,omitempty"` // includes Violations (the proof)
	Strict     bool               `json:"strict,omitempty"`
	DraftID    string             `json:"draft_id,omitempty"`
	Detail     string             `json:"detail,omitempty"` // e.g. reject reason
}

// AuditSink receives audit events. Implementations MUST be safe for concurrent
// use: analysis events are emitted from parallel goroutines during Analyze.
type AuditSink interface {
	Record(ctx context.Context, e AuditEvent) error
}

// WithAuditSink registers a durable audit sink.
func WithAuditSink(s AuditSink) Option {
	return func(e *Engine) { e.audit = s }
}

// emitAudit records an event if a sink is configured. The error is intentionally
// not propagated into analysis/approval flow (a transient audit hiccup should
// not fail an insight or an approval); hosts needing audit-before-ack semantics
// implement that inside Record (durable write, retry, dead-letter, or panic).
func (e *Engine) emitAudit(ctx context.Context, ev AuditEvent) {
	if e.audit == nil {
		return
	}
	_ = e.audit.Record(ctx, ev)
}

// ---- Reference sink: in-memory, append-only, hash-chained -----------------

// AuditEntry is a stored event with tamper-evidence chain links.
type AuditEntry struct {
	AuditEvent
	PrevHash string `json:"prev_hash"`
	Hash     string `json:"hash"`
}

// MemoryAuditSink is a reference AuditSink: an in-process, append-only,
// hash-chained log. Each entry's Hash covers the event plus the previous
// entry's Hash, so editing any past entry breaks the chain (Verify detects it).
// It is NOT durable across restarts — it demonstrates the seam and powers the
// demo's audit panel. Real hosts implement AuditSink over a durable store
// (and can reuse this hash-chaining for tamper-evidence).
type MemoryAuditSink struct {
	mu      sync.Mutex
	entries []AuditEntry
}

// NewMemoryAuditSink returns an empty hash-chained in-memory sink.
func NewMemoryAuditSink() *MemoryAuditSink { return &MemoryAuditSink{} }

// Record appends an event, linking it into the hash chain.
func (m *MemoryAuditSink) Record(_ context.Context, e AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := ""
	if n := len(m.entries); n > 0 {
		prev = m.entries[n-1].Hash
	}
	m.entries = append(m.entries, AuditEntry{AuditEvent: e, PrevHash: prev, Hash: chainHash(prev, e)})
	return nil
}

// Entries returns a snapshot of the log, oldest first.
func (m *MemoryAuditSink) Entries() []AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AuditEntry, len(m.entries))
	copy(out, m.entries)
	return out
}

// Verify recomputes the chain and reports whether it is intact (no entry has
// been altered or reordered since it was recorded).
func (m *MemoryAuditSink) Verify() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := ""
	for _, en := range m.entries {
		if en.PrevHash != prev || en.Hash != chainHash(prev, en.AuditEvent) {
			return false
		}
		prev = en.Hash
	}
	return true
}

func chainHash(prev string, e AuditEvent) string {
	b, _ := json.Marshal(e)
	sum := sha256.Sum256(append([]byte(prev), b...))
	return hex.EncodeToString(sum[:])
}
