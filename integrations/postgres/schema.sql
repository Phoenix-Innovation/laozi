-- Schema for the Laozi Postgres integrations.
-- Run against a Postgres with the timescaledb and vector extensions available.

-- ---------------------------------------------------------------------------
-- Durable, hash-chained audit (TimescaleDB hypertable)
-- ---------------------------------------------------------------------------
CREATE EXTENSION IF NOT EXISTS timescaledb;

CREATE TABLE IF NOT EXISTS laozi_audit (
    seq         BIGSERIAL,
    time        TIMESTAMPTZ NOT NULL,
    kind        TEXT        NOT NULL,   -- analysis | draft_proposed | draft_approved | draft_rejected
    actor       TEXT,                   -- who (drafts); empty for analysis
    category_id TEXT,
    draft_id    TEXT,
    payload     JSONB       NOT NULL,   -- full AuditEvent; the hash is taken over this
    prev_hash   TEXT        NOT NULL,
    hash        TEXT        NOT NULL,
    PRIMARY KEY (seq, time)             -- hypertable PK must include the time column
);

SELECT create_hypertable('laozi_audit', 'time', if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS laozi_audit_kind_time_idx ON laozi_audit (kind, time DESC);
CREATE INDEX IF NOT EXISTS laozi_audit_actor_idx     ON laozi_audit (actor, time DESC);

-- ---------------------------------------------------------------------------
-- RAG documents (pgvector)
-- ---------------------------------------------------------------------------
CREATE EXTENSION IF NOT EXISTS vector;

-- vector(1536) matches OpenAI text-embedding-3-small. Change N to match your
-- embedder (text-embedding-3-large = 3072).
CREATE TABLE IF NOT EXISTS laozi_rag_documents (
    id         BIGSERIAL PRIMARY KEY,
    content    TEXT  NOT NULL,
    source     TEXT,
    source_url TEXT,
    embedding  VECTOR(1536) NOT NULL
);

-- Approximate nearest-neighbour index for cosine distance (the <=> operator).
-- Build it after loading a representative amount of data; tune `lists`.
CREATE INDEX IF NOT EXISTS laozi_rag_embedding_idx
    ON laozi_rag_documents USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);
