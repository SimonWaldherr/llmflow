package llm

// anthropic.go implements Anthropic's Messages API.
// Docs: https://docs.anthropic.com/en/api/messages

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

const anthropicVersion = "2023-06-01"

type anthropicClient struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	model      string
	maxOut     int64
}

func newAnthropicClient(cfg config.APIConfig, apiKey string) *anthropicClient {
	return &anthropicClient{
		httpClient: &http.Client{Timeout: cfg.Timeout},
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:     apiKey,
		model:      cfg.Model,
		maxOut:     cfg.MaxOutputTokens,
	}
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	MaxTokens int64              `json:"max_tokens"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *anthropicClient) Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	maxTok := c.maxOut
	if maxTok <= 0 {
		maxTok = 1024
	}
	payload := anthropicRequest{
		Model:     c.model,
		System:    systemPrompt,
		Messages:  []anthropicMessage{{Role: "user", Content: userPrompt}},
		MaxTokens: maxTok,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

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
		return "", fmt.Errorf("anthropic api status %d: %s", resp.StatusCode, string(respBody))
	}

	var decoded anthropicResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if decoded.Error != nil {
		return "", fmt.Errorf("anthropic error (%s): %s", decoded.Error.Type, decoded.Error.Message)
	}
	for _, block := range decoded.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("no text content returned by anthropic")
}
