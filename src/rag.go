package laozi

import (
	"context"
	"math"
	"sort"
	"strings"
)

// InMemoryRAG is a simple in-memory RAG store using cosine similarity
type InMemoryRAG struct {
	documents []ragDocument
}

type ragDocument struct {
	Content   string
	Source    string
	SourceURL string
	Embedding []float64
	Category  string
}

// NewInMemoryRAG creates a new in-memory RAG store
func NewInMemoryRAG() *InMemoryRAG {
	return &InMemoryRAG{
		documents: make([]ragDocument, 0),
	}
}

// Add adds a document to the store with simple TF-IDF-style embedding
func (r *InMemoryRAG) Add(content, source, sourceURL, category string) {
	r.documents = append(r.documents, ragDocument{
		Content:   content,
		Source:    source,
		SourceURL: sourceURL,
		Embedding: simpleEmbed(content),
		Category:  category,
	})
}

// Search finds the most relevant documents for a query
func (r *InMemoryRAG) Search(ctx context.Context, query string, limit int) ([]RAGResult, error) {
	if len(r.documents) == 0 {
		return nil, nil
	}

	queryEmbed := simpleEmbed(query)

	type scored struct {
		doc   ragDocument
		score float64
	}

	var scores []scored
	for _, doc := range r.documents {
		score := cosineSimilarity(queryEmbed, doc.Embedding)
		scores = append(scores, scored{doc: doc, score: score})
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	if limit > len(scores) {
		limit = len(scores)
	}

	results := make([]RAGResult, limit)
	for i := 0; i < limit; i++ {
		results[i] = RAGResult{
			Content:   scores[i].doc.Content,
			Source:    scores[i].doc.Source,
			SourceURL: scores[i].doc.SourceURL,
			Score:     scores[i].score,
		}
	}

	return results, nil
}

// simpleEmbed creates a simple bag-of-words embedding
func simpleEmbed(text string) []float64 {
	const dim = 256
	embed := make([]float64, dim)

	words := strings.Fields(strings.ToLower(text))
	for _, word := range words {
		// Simple hash to dimension
		h := hashString(word) % dim
		embed[h] += 1.0
	}

	// Normalize
	var mag float64
	for _, v := range embed {
		mag += v * v
	}
	mag = math.Sqrt(mag)
	if mag > 0 {
		for i := range embed {
			embed[i] /= mag
		}
	}

	return embed
}

func hashString(s string) int {
	h := 0
	for _, c := range s {
		h = 31*h + int(c)
	}
	if h < 0 {
		h = -h
	}
	return h
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
