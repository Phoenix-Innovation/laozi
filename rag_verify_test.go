package laozi

import (
	"context"
	"testing"
)

func TestInMemoryRAGSatisfiesInterface(t *testing.T) {
	var _ RAGStore = NewInMemoryRAG()
}

func TestInMemoryRAGSearchReturnsRAGResult(t *testing.T) {
	rag := NewInMemoryRAG(WithRAGMinSimilarity(0.1))
	rag.Add(RAGResult{
		Content:   "Fasting glucose above 100 mg/dL indicates prediabetes per ADA guidelines",
		Source:    "ADA",
		SourceURL: "https://diabetes.org/diagnosis",
	})
	rag.Add(RAGResult{
		Content:   "Blood pressure above 120/80 is elevated per AHA guidelines",
		Source:    "AHA",
		SourceURL: "https://heart.org/bp-guidelines",
	})
	rag.Add(RAGResult{
		Content:   "Regular exercise of 150 minutes per week reduces cardiovascular risk",
		Source:    "CDC",
		SourceURL: "https://cdc.gov/physical-activity",
	})

	results, err := rag.Search(context.Background(), "glucose prediabetes diabetes", 2)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	// Top result should have Score > 0
	if results[0].Score <= 0 {
		t.Errorf("expected positive Score, got %f", results[0].Score)
	}
	// Verify it's a RAGResult (has all fields)
	_ = results[0].Content
	_ = results[0].Source
	_ = results[0].SourceURL
	_ = results[0].Score
	t.Logf("Top result: Source=%s Score=%.3f", results[0].Source, results[0].Score)
}

func TestInMemoryRAGWithOptions(t *testing.T) {
	rag := NewInMemoryRAG(
		WithRAGTopK(1),
		WithRAGMinSimilarity(0.0), // accept everything
		WithRAGEmbeddingDim(128),
	)
	rag.Add(RAGResult{Content: "test doc", Source: "test", SourceURL: "https://test.com"})
	rag.Add(RAGResult{Content: "another doc", Source: "test2", SourceURL: "https://test2.com"})

	results, err := rag.Search(context.Background(), "test", 0) // 0 → use topK=1
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected topK=1 to return 1 result, got %d", len(results))
	}
}

func TestInMemoryRAGEmptyStore(t *testing.T) {
	rag := NewInMemoryRAG()
	results, err := rag.Search(context.Background(), "anything", 5)
	if err != nil {
		t.Fatalf("Search on empty store should not error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results from empty store, got %d", len(results))
	}
}
