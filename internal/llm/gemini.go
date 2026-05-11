package llm

// gemini.go implements Google Gemini's generateContent REST API.
// Docs: https://ai.google.dev/api/generate-content

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

type geminiClient struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	model      string
	maxOut     int64
}

func newGeminiClient(cfg config.APIConfig, apiKey string) *geminiClient {
	return &geminiClient{
		httpClient: &http.Client{Timeout: cfg.Timeout},
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:     apiKey,
		model:      cfg.Model,
		maxOut:     cfg.MaxOutputTokens,
	}
}

// Gemini REST request/response structures.

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiRequest struct {
	SystemInstruction *geminiContent   `json:"system_instruction,omitempty"`
	Contents          []geminiContent  `json:"contents"`
	GenerationConfig  *geminiGenConfig `json:"generation_config,omitempty"`
}

type geminiGenConfig struct {
	MaxOutputTokens int64 `json:"maxOutputTokens,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content geminiContent `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error,omitempty"`
}

func (c *geminiClient) Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	req := geminiRequest{
		Contents: []geminiContent{
			{Role: "user", Parts: []geminiPart{{Text: userPrompt}}},
		},
	}
	if systemPrompt != "" {
		req.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: systemPrompt}},
		}
	}
	if c.maxOut > 0 {
		req.GenerationConfig = &geminiGenConfig{MaxOutputTokens: c.maxOut}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	// URL: {baseURL}/models/{model}:generateContent?key={apiKey}
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", c.baseURL, c.model, c.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode >= 300 {
		return "", apiStatusError("gemini", resp, respBody)
	}

	var decoded geminiResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if decoded.Error != nil {
		return "", apiResponseError("gemini", decoded.Error.Code, "", decoded.Error.Message)
	}
	if len(decoded.Candidates) == 0 || len(decoded.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no content returned by gemini")
	}
	return decoded.Candidates[0].Content.Parts[0].Text, nil
}
