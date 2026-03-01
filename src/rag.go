package laozi

import (
	"context"
	"math"
	"sort"
	"strings"
)

// ================================================================================
// IN-MEMORY RAG STORE (uses config.go settings)
// ================================================================================

// InMemoryRAG is a simple vector store for development/testing
type InMemoryRAG struct {
	documents []storedDoc
}

type storedDoc struct {
	doc       RAGDocument
	embedding []float64
}

// NewInMemoryRAG creates an in-memory RAG store
func NewInMemoryRAG() *InMemoryRAG {
	return &InMemoryRAG{
		documents: make([]storedDoc, 0),
	}
}

// Add stores a document with its embedding
func (r *InMemoryRAG) Add(doc RAGDocument) {
	emb := simpleEmbedding(doc.Content)
	r.documents = append(r.documents, storedDoc{doc: doc, embedding: emb})
}

// Search finds the top-K most similar documents (uses RAGTopK from config)
func (r *InMemoryRAG) Search(ctx context.Context, query string, topK int) ([]RAGDocument, error) {
	if topK <= 0 {
		topK = RAGTopK // Use config default
	}

	queryEmb := simpleEmbedding(query)

	type scored struct {
		doc        RAGDocument
		similarity float64
	}

	scores := make([]scored, 0, len(r.documents))
	for _, sd := range r.documents {
		sim := cosineSimilarity(queryEmb, sd.embedding)
		if sim >= RAGMinSimilarity { // Use config threshold
			sd.doc.Similarity = sim
			scores = append(scores, scored{doc: sd.doc, similarity: sim})
		}
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].similarity > scores[j].similarity
	})

	results := make([]RAGDocument, 0, topK)
	for i := 0; i < len(scores) && i < topK; i++ {
		results = append(results, scores[i].doc)
	}

	return results, nil
}

// ================================================================================
// EMBEDDING (simple TF-IDF style, uses RAGEmbeddingDim from config)
// ================================================================================

func simpleEmbedding(text string) []float64 {
	words := strings.Fields(strings.ToLower(text))
	emb := make([]float64, RAGEmbeddingDim)

	for _, word := range words {
		idx := hashWord(word) % RAGEmbeddingDim
		emb[idx] += 1.0
	}

	// Add bigrams
	for i := 0; i < len(words)-1; i++ {
		bigram := words[i] + "_" + words[i+1]
		idx := hashWord(bigram) % RAGEmbeddingDim
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
	hash := 0
	for _, c := range word {
		hash = hash*31 + int(c)
	}
	if hash < 0 {
		hash = -hash
	}
	return hash
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
