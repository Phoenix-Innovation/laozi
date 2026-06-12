package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	laozi "github.com/Phoenix-Innovation/laozi"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time proof this satisfies the laozi seam.
var _ laozi.AuditSink = (*TimescaleAuditSink)(nil)

const defaultAuditTable = "laozi_audit"

// TimescaleAuditSink persists audit events to a TimescaleDB hypertable. Each
// row is hash-chained (sha256 over the previous row's hash plus this event's
// canonical JSON), the same scheme as laozi.MemoryAuditSink, so a tampered or
// reordered row is detectable with Verify. See schema.sql for the table.
type TimescaleAuditSink struct {
	pool  *pgxpool.Pool
	table string
}

// AuditOption configures a TimescaleAuditSink.
type AuditOption func(*TimescaleAuditSink)

// WithAuditTable overrides the table name (default "laozi_audit").
// A name that is not a safe identifier is ignored, leaving the default.
func WithAuditTable(name string) AuditOption {
	return func(s *TimescaleAuditSink) {
		if safePgIdent(name) {
			s.table = name
		}
	}
}

// NewTimescaleAuditSink returns a sink writing to the given pool.
func NewTimescaleAuditSink(pool *pgxpool.Pool, opts ...AuditOption) *TimescaleAuditSink {
	s := &TimescaleAuditSink{pool: pool, table: defaultAuditTable}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Record appends one event. The chain append is serialized with a
// transaction-scoped advisory lock so concurrent analysis goroutines link
// cleanly. The hash is taken over the exact JSON stored in the payload column,
// so verification never depends on a struct round-trip.
func (s *TimescaleAuditSink) Record(ctx context.Context, e laozi.AuditEvent) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	// Serialize chain appends. hashtext() maps the table name to a lock key.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, s.table); err != nil {
		return err
	}

	var prev string
	err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT hash FROM %s ORDER BY seq DESC LIMIT 1`, s.table)).Scan(&prev)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	h := chainHash(prev, payload)
	_, err = tx.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (time, kind, actor, category_id, draft_id, payload, prev_hash, hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, s.table),
		e.Time, e.Kind, e.Actor, e.CategoryID, e.DraftID, payload, prev, h)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Verify recomputes the chain from the table and reports whether it is intact
// (no row altered, removed, or reordered since it was written).
func (s *TimescaleAuditSink) Verify(ctx context.Context) (bool, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`SELECT prev_hash, hash, payload FROM %s ORDER BY seq ASC`, s.table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	prev := ""
	for rows.Next() {
		var ph, h string
		var payload []byte
		if err := rows.Scan(&ph, &h, &payload); err != nil {
			return false, err
		}
		if ph != prev || h != chainHash(prev, payload) {
			return false, nil
		}
		prev = h
	}
	return true, rows.Err()
}

func chainHash(prev string, payload []byte) string {
	sum := sha256.Sum256(append([]byte(prev), payload...))
	return hex.EncodeToString(sum[:])
}
