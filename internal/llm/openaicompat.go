package llm

// openaicompat.go implements the OpenAI Chat Completions wire format which is
// shared by OpenAI, LM Studio, Ollama (/v1), and many other compatible endpoints.

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

type openAICompatClient struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	model      string
	maxOut     int64
}

func newOpenAICompatClient(cfg config.APIConfig, apiKey string) *openAICompatClient {
	return &openAICompatClient{
		httpClient: &http.Client{Timeout: cfg.Timeout},
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:     apiKey,
		model:      cfg.Model,
		maxOut:     cfg.MaxOutputTokens,
	}
}

// chatRequest is the OpenAI Chat Completions request body.
type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int64         `json:"max_tokens,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

func (c *openAICompatClient) Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	messages := []chatMessage{}
	if systemPrompt != "" {
		messages = append(messages, chatMessage{Role: "system", Content: systemPrompt})
	}
	messages = append(messages, chatMessage{Role: "user", Content: userPrompt})

	payload := chatRequest{
		Model:     c.model,
		Messages:  messages,
		MaxTokens: c.maxOut,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
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
		return "", fmt.Errorf("api status %d: %s", resp.StatusCode, string(respBody))
	}

	var decoded chatResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if decoded.Error != nil {
		return "", fmt.Errorf("api error (%s): %s", decoded.Error.Type, decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("no choices returned by api")
	}
	return decoded.Choices[0].Message.Content, nil
}
