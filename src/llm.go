package laozi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ================================================================================
// DEFAULT LLM CLIENT (uses config.go settings)
// ================================================================================

// DefaultLLMClient uses the build-time configuration
type DefaultLLMClient struct {
	client *http.Client
}

// NewDefaultLLMClient creates a client using config.go settings
func NewDefaultLLMClient() *DefaultLLMClient {
	return &DefaultLLMClient{
		client: &http.Client{},
	}
}

func (c *DefaultLLMClient) Chat(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	apiKey := GetAPIKey()
	if apiKey == "" {
		return "", fmt.Errorf("no API key: set LAOZI_API_KEY environment variable")
	}

	reqBody := map[string]interface{}{
		"model": LLMModel,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": LLMTemperature,
		"max_tokens":  LLMMaxTokens,
		"top_p":       LLMTopP,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal error: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", LLMEndpoint, bytes.NewReader(jsonBody))
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
