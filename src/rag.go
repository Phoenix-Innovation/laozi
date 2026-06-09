package laozi

import (
        "context"
        "math"
        "sort"
        "strings"
)

// ================================================================================
// IN-MEMORY RAG STORE
// Compatible with RAGStore interface from laozi.go (returns []RAGResult).
// Suitable for development/testing; swap with a real vector DB in production.
// ================================================================================

// Default RAG constants (used when no Config override is supplied).
//
// NOTE: defaultRAGMinSimilarity is set low because simpleEmbedding is a
// bag-of-words hash — cosine similarities between short texts rarely exceed
// 0.3. A threshold of 0.7 effectively disables RAG. For production use a
// real embedding model (e.g. text-embedding-3-small) and raise this to 0.5+.
const (
        defaultRAGTopK          = 3
        defaultRAGMinSimilarity = 0.15
        defaultRAGEmbeddingDim  = 384
)

// InMemoryRAG is a simple in-memory vector store for development/testing.
type InMemoryRAG struct {
        documents    []storedDoc
        topK         int
        minSimilarity float64
        embeddingDim  int
}

type storedDoc struct {
        result    RAGResult
        embedding []float64
}

// RAGOption configures an InMemoryRAG instance.
type RAGOption func(*InMemoryRAG)

// WithRAGTopK overrides the default number of results returned by Search.
func WithRAGTopK(k int) RAGOption {
        return func(r *InMemoryRAG) { r.topK = k }
}

// WithRAGMinSimilarity sets the minimum cosine-similarity threshold.
func WithRAGMinSimilarity(s float64) RAGOption {
        return func(r *InMemoryRAG) { r.minSimilarity = s }
}

// WithRAGEmbeddingDim sets the embedding vector dimension.
func WithRAGEmbeddingDim(d int) RAGOption {
        return func(r *InMemoryRAG) { r.embeddingDim = d }
}

// NewInMemoryRAG creates an in-memory RAG store.
// It satisfies the RAGStore interface declared in laozi.go.
func NewInMemoryRAG(opts ...RAGOption) *InMemoryRAG {
        r := &InMemoryRAG{
                documents:    make([]storedDoc, 0),
                topK:         defaultRAGTopK,
                minSimilarity: defaultRAGMinSimilarity,
                embeddingDim:  defaultRAGEmbeddingDim,
        }
        for _, o := range opts {
                o(r)
        }
        return r
}

// Add stores a document with its embedding.
// The caller builds a RAGResult; the Score field is ignored on input
// (it is populated at search time).
func (r *InMemoryRAG) Add(doc RAGResult) {
        emb := simpleEmbedding(doc.Content, r.embeddingDim)
        r.documents = append(r.documents, storedDoc{result: doc, embedding: emb})
}

// Search finds the top-K most similar documents by cosine similarity.
// It satisfies RAGStore.Search(ctx, query, limit) ([]RAGResult, error).
func (r *InMemoryRAG) Search(ctx context.Context, query string, limit int) ([]RAGResult, error) {
        topK := limit
        if topK <= 0 {
                topK = r.topK
        }

        queryEmb := simpleEmbedding(query, r.embeddingDim)

        type scored struct {
                result     RAGResult
                similarity float64
        }

        scores := make([]scored, 0, len(r.documents))
        for _, sd := range r.documents {
                sim := cosineSimilarity(queryEmb, sd.embedding)
                if sim >= r.minSimilarity {
                        res := sd.result
                        res.Score = sim
                        scores = append(scores, scored{result: res, similarity: sim})
                }
        }

        sort.Slice(scores, func(i, j int) bool {
                return scores[i].similarity > scores[j].similarity
        })

        results := make([]RAGResult, 0, topK)
        for i := 0; i < len(scores) && i < topK; i++ {
                results = append(results, scores[i].result)
        }

        return results, nil
}

// ================================================================================
// EMBEDDING (simple TF-IDF style with bigrams)
// ================================================================================

func simpleEmbedding(text string, dim int) []float64 {
        words := strings.Fields(strings.ToLower(text))
        emb := make([]float64, dim)

        for _, word := range words {
                idx := hashWord(word) % dim
                emb[idx] += 1.0
        }

        // Add bigrams for better context capture
        for i := 0; i < len(words)-1; i++ {
                bigram := words[i] + "_" + words[i+1]
                idx := hashWord(bigram) % dim
                emb[idx] += 0.5
        }

        // Normalize
        var mag float64
        for _, v := range emb {
                mag += v * v
        }
        if mag > 0 {
                mag = math.Sqrt(mag)
                for i := range emb {
                        emb[i] /= mag
                }
        }

        return emb
}

func hashWord(word string) int {
        // Use uint arithmetic to avoid overflow into math.MinInt, which
        // stays negative after negation and would cause a negative modulo index.
        var h uint
        for _, c := range word {
                h = h*31 + uint(c)
        }
        return int(h & 0x7FFFFFFF) // always non-negative
}

func cosineSimilarity(a, b []float64) float64 {
        if len(a) != len(b) {
                return 0
        }

        var dot, magA, magB float64
        for i := range a {
                dot += a[i] * b[i]
                magA += a[i] * a[i]
                magB += b[i] * b[i]
        }

        if magA == 0 || magB == 0 {
                return 0
        }

        return dot / (math.Sqrt(magA) * math.Sqrt(magB))
}
