# Lao Zi Postgres integrations

Reference implementations of the two Lao Zi seams over Postgres:

- `TimescaleAuditSink` — a durable, hash-chained `laozi.AuditSink` backed by a TimescaleDB hypertable.
- `PgVectorRAG` — a `laozi.RAGStore` backed by pgvector, with a pluggable `Embedder` (`OpenAIEmbedder` included, stdlib-only).

This is a **separate module** so the core `laozi` package stays dependency-free. It pulls in `github.com/jackc/pgx/v5`.

## Setup

```bash
psql "$DATABASE_URL" -f schema.sql      # creates the hypertable + pgvector table
go mod tidy                             # resolve pgx
```

The RAG table's `vector(1536)` must match your embedder's dimension (1536 for `text-embedding-3-small`).

## Wiring it in

```go
import (
    "github.com/jackc/pgx/v5/pgxpool"
    laozi "github.com/Phoenix-Innovation/laozi"
    pg "github.com/Phoenix-Innovation/laozi/integrations/postgres"
)

pool, _ := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))

audit := pg.NewTimescaleAuditSink(pool)
rag := pg.NewPgVectorRAG(pool,
    pg.OpenAIEmbedder{APIKey: os.Getenv("LAOZI_API_KEY")},
    pg.WithMinSimilarity(0.75),
)

engine := laozi.New(
    laozi.WithLLM(laozi.NewDefaultLLMClient()),
    laozi.WithRAG(rag),
    laozi.WithAuditSink(audit),
)
```

Ingest documents once with `rag.Add(ctx, laozi.RAGResult{Content: ..., Source: ..., SourceURL: ...})`.

## How the audit chain works

Every event is written with `prev_hash` and `hash`, where `hash = sha256(prev_hash + payload_json)` — the same scheme as `laozi.MemoryAuditSink`. Appends are serialized with a transaction-scoped advisory lock so concurrent analysis goroutines link in order. `Verify(ctx)` recomputes the chain over the stored `payload` column (no struct round-trip), so any altered, deleted, or reordered row is detected.

## Notes

`PgVectorRAG` ranks by cosine similarity via the `<=>` operator and reports `Score = 1 - cosine_distance`. Vectors are sent in pgvector's text form (`[v1,v2,...]`) cast with `$n::vector`, which keeps the dependency to `pgx` alone.

`OpenAIEmbedder` is a convenience. Any type implementing `Embedder` works — a local model, a different provider, or a cache in front of one.
