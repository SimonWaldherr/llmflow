package llm

// ollama.go implements Ollama's native /api/generate endpoint.
// Ollama also exposes an OpenAI-compatible /v1/chat/completions endpoint,
// but the native endpoint is used here for better streaming-free operation
// with show_full_output.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type ollamaClient struct {
	httpClient *http.Client
	baseURL    string
	model      string
	apiKey     string // optional (for secured/proxied Ollama)
}

func newOllamaClient(cfg config.APIConfig) *ollamaClient {
	return &ollamaClient{
		httpClient: &http.Client{Timeout: cfg.Timeout},
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		model:      cfg.Model,
	}
}

type ollamaGenerateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	System string `json:"system,omitempty"`
	Stream bool   `json:"stream"`
}

type ollamaGenerateResponse struct {
	Response string `json:"response"`
	Error    string `json:"error,omitempty"`
}

func (c *ollamaClient) Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	payload := ollamaGenerateRequest{
		Model:  c.model,
		Prompt: userPrompt,
		System: systemPrompt,
		Stream: false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode >= 300 {
		return "", apiStatusError("ollama", resp, respBody)
	}

	var decoded ollamaGenerateResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if decoded.Error != "" {
		return "", apiResponseError("ollama", 0, "", decoded.Error)
	}
	return decoded.Response, nil
}
