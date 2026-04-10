package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	model      string
	maxOut     int64
}

type responsesRequest struct {
	Model           string `json:"model"`
	Instructions    string `json:"instructions,omitempty"`
	Input           string `json:"input"`
	MaxOutputTokens int64  `json:"max_output_tokens,omitempty"`
}

type responsesResponse struct {
	OutputText string `json:"output_text"`
	Error      *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

func New(cfg config.APIConfig, apiKey string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: cfg.Timeout},
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:     apiKey,
		model:      cfg.Model,
		maxOut:     cfg.MaxOutputTokens,
	}
}

func (c *Client) Generate(ctx context.Context, systemPrompt, input string) (string, error) {
	payload := responsesRequest{
		Model:           c.model,
		Instructions:    systemPrompt,
		Input:           input,
		MaxOutputTokens: c.maxOut,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	url := c.baseURL + "/responses"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

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
		return "", fmt.Errorf("responses api status %d: %s", resp.StatusCode, string(respBody))
	}
	var decoded responsesResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if decoded.Error != nil {
		return "", fmt.Errorf("responses api error (%s): %s", decoded.Error.Type, decoded.Error.Message)
	}
	return decoded.OutputText, nil
}

func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<uint(attempt-1)) * time.Second
}
