package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	laozi "github.com/Phoenix-Innovation/laozi"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time proof this satisfies the laozi seam.
var _ laozi.RAGStore = (*PgVectorRAG)(nil)

// Embedder turns text into a vector. Plug in OpenAI (see OpenAIEmbedder), a
// local model, etc. Its output dimension must match the table's vector(N)
// column in schema.sql.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

const defaultRAGTable = "laozi_rag_documents"

// Search embeds thequery and ranks documents by cosine similarity using the pgvector <=>
// operator. See schema.sql.
type PgVectorRAG struct {
	pool          *pgxpool.Pool
	embed         Embedder
	table         string
	minSimilarity float64
}

type RAGOption func(*PgVectorRAG)

// WithRAGTable overrides the table name (default "laozi_rag_documents").
func WithRAGTable(name string) RAGOption { return func(r *PgVectorRAG) { r.table = name } }

// WithMinSimilarity drops results below the given cosine similarity (0..1).
func WithMinSimilarity(s float64) RAGOption { return func(r *PgVectorRAG) { r.minSimilarity = s } }

// NewPgVectorRAG returns a store using the given pool and embedder.
func NewPgVectorRAG(pool *pgxpool.Pool, embed Embedder, opts ...RAGOption) *PgVectorRAG {
	r := &PgVectorRAG{pool: pool, embed: embed, table: defaultRAGTable}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Add embeds the document's content and stores it.
func (r *PgVectorRAG) Add(ctx context.Context, doc laozi.RAGResult) error {
	vec, err := r.embed.Embed(ctx, doc.Content)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (content, source, source_url, embedding)
		VALUES ($1, $2, $3, $4::vector)`, r.table),
		doc.Content, doc.Source, doc.SourceURL, vectorLiteral(vec))
	return err
}

// Search embeds the query and returns the top-K most similar documents by
// cosine similarity. Score is 1 - cosine_distance.
func (r *PgVectorRAG) Search(ctx context.Context, query string, limit int) ([]laozi.RAGResult, error) {
	vec, err := r.embed.Embed(ctx, query)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		SELECT content, source, source_url, 1 - (embedding <=> $1::vector) AS score
		FROM %s
		WHERE 1 - (embedding <=> $1::vector) >= $2
		ORDER BY embedding <=> $1::vector
		LIMIT $3`, r.table),
		vectorLiteral(vec), r.minSimilarity, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []laozi.RAGResult
	for rows.Next() {
		var res laozi.RAGResult
		if err := rows.Scan(&res.Content, &res.Source, &res.SourceURL, &res.Score); err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

// vectorLiteral formats a vector in pgvector's text form, "[v1,v2,...]", which
// is cast with $n::vector in the queries above. No pgvector-go type registration required.
func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.Grow(len(v)*8 + 2)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
