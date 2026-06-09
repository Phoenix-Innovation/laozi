package laozi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// ================================================================================
// DEFAULT LLM CLIENT
// ================================================================================

// DefaultLLMClient sends Chat requests to an OpenAI-compatible endpoint.
// API key is read from the LAOZI_API_KEY environment variable (via GetAPIKey).
type DefaultLLMClient struct {
	client   *http.Client
	Model    string  // e.g. "gpt-4o-mini" (default)
	Endpoint string  // e.g. "https://api.openai.com/v1/chat/completions"
	Temp     float64 // sampling temperature (default 0.3)
	MaxTok   int     // max tokens per response (default 500)
	TopP     float64 // nucleus sampling (default 0.9)
}

// DefaultLLMOption configures a DefaultLLMClient.
type DefaultLLMOption func(*DefaultLLMClient)

// WithModel sets the model name.
func WithModel(m string) DefaultLLMOption {
	return func(c *DefaultLLMClient) { c.Model = m }
}

// WithEndpoint sets the API endpoint URL.
func WithEndpoint(u string) DefaultLLMOption {
	return func(c *DefaultLLMClient) { c.Endpoint = u }
}

// WithTemperature sets the sampling temperature.
func WithTemperature(t float64) DefaultLLMOption {
	return func(c *DefaultLLMClient) { c.Temp = t }
}

// WithMaxTokens sets the maximum response tokens.
func WithMaxTokens(n int) DefaultLLMOption {
	return func(c *DefaultLLMClient) { c.MaxTok = n }
}

// WithTopP sets the nucleus-sampling parameter.
func WithTopP(p float64) DefaultLLMOption {
	return func(c *DefaultLLMClient) { c.TopP = p }
}

// NewDefaultLLMClient creates a client with sensible defaults.
// Override any field via DefaultLLMOption functional options.
func NewDefaultLLMClient(opts ...DefaultLLMOption) *DefaultLLMClient {
	c := &DefaultLLMClient{
		client:   &http.Client{},
		Model:    "gpt-4o-mini",
		Endpoint: "https://api.openai.com/v1/chat/completions",
		Temp:     0.3,
		MaxTok:   500,
		TopP:     0.9,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Chat sends a system+user prompt pair to the configured endpoint.
// It satisfies the LLMClient interface.
func (c *DefaultLLMClient) Chat(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	apiKey := GetAPIKey()
	if apiKey == "" {
		return "", fmt.Errorf("no API key: set LAOZI_API_KEY environment variable")
	}

	reqBody := map[string]interface{}{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": c.Temp,
		"max_tokens":  c.MaxTok,
		"top_p":       c.TopP,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal error: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.Endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("request error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP error: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read error: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("unmarshal error: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	return result.Choices[0].Message.Content, nil
}

// GetAPIKey reads the API key from the LAOZI_API_KEY environment variable.
func GetAPIKey() string {
	return os.Getenv("LAOZI_API_KEY")
}
