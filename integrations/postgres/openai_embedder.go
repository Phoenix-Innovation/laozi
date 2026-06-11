package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// OpenAIEmbedder is a ready Embedder using OpenAI's embeddings endpoint. 
// Set Model to match the vector(N) column in schema.sql:
// text-embedding-3-small is 1536 dims, text-embedding-3-large is 3072.
type OpenAIEmbedder struct {
	APIKey   string
	Model    string       // default "text-embedding-3-small"
	Endpoint string       // default "https://api.openai.com/v1/embeddings"
	HTTP     *http.Client // default http.DefaultClient
}

// Returns the embedding for text.
func (e OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	model := e.Model
	if model == "" {
		model = "text-embedding-3-small"
	}
	endpoint := e.Endpoint
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1/embeddings"
	}
	client := e.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	body, _ := json.Marshal(map[string]any{"model": model, "input": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings: status %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("embeddings: empty response")
	}
	return out.Data[0].Embedding, nil
}
