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
const anthropicCachingBeta = "prompt-caching-2024-07-31"

type anthropicClient struct {
	httpClient    *http.Client
	baseURL       string
	apiKey        string
	model         string
	maxOut        int64
	promptCaching bool
}

func newAnthropicClient(cfg config.APIConfig, apiKey string) *anthropicClient {
	return &anthropicClient{
		httpClient:    &http.Client{Timeout: cfg.Timeout},
		baseURL:       strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:        apiKey,
		model:         cfg.Model,
		maxOut:        cfg.MaxOutputTokens,
		promptCaching: cfg.PromptCaching,
	}
}

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

// anthropicRequest is the top-level body sent to /messages.
// System can be a plain string or, when prompt caching is enabled, a slice of
// anthropicSystemBlock so individual segments carry cache_control markers.
type anthropicRequest struct {
	Model     string             `json:"model"`
	System    any                `json:"system,omitempty"` // string | []anthropicSystemBlock
	Messages  []anthropicMessage `json:"messages"`
	MaxTokens int64              `json:"max_tokens"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []anthropicBlock
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

// anthropicCacheControl marks a block as cacheable (ephemeral).
type anthropicCacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// anthropicSystemBlock is used when prompt caching is enabled so individual
// system-prompt segments can carry cache_control markers.
type anthropicSystemBlock struct {
	Type         string                 `json:"type"` // "text"
	Text         string                 `json:"text"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

// anthropicBlock covers text, tool_use, and tool_result content blocks.
type anthropicBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`          // tool_use
	Name      string `json:"name,omitempty"`        // tool_use
	Input     any    `json:"input,omitempty"`       // tool_use
	ToolUseID string `json:"tool_use_id,omitempty"` // tool_result
	Content   string `json:"content,omitempty"`     // tool_result
}

type anthropicResponse struct {
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// Generator (simple, single-turn)
// ---------------------------------------------------------------------------

func (c *anthropicClient) Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	maxTok := c.maxOut
	if maxTok <= 0 {
		maxTok = 1024
	}

	// Build system field: a plain string normally, or a block with cache_control
	// when prompt caching is enabled (the system prompt is the primary cache target
	// because it stays identical across all records in a run).
	var systemField any = systemPrompt
	if c.promptCaching && systemPrompt != "" {
		systemField = []anthropicSystemBlock{
			{Type: "text", Text: systemPrompt, CacheControl: &anthropicCacheControl{Type: "ephemeral"}},
		}
	}

	payload := anthropicRequest{
		Model:     c.model,
		System:    systemField,
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
	if c.promptCaching {
		req.Header.Set("anthropic-beta", anthropicCachingBeta)
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
		return "", apiStatusError("anthropic", resp, respBody)
	}

	var decoded anthropicResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if decoded.Error != nil {
		return "", apiResponseError("anthropic", 0, decoded.Error.Type, decoded.Error.Message)
	}
	for _, block := range decoded.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("no text content returned by anthropic")
}

// ---------------------------------------------------------------------------
// AgentGenerator (multi-turn with tool calling)
// ---------------------------------------------------------------------------

func (c *anthropicClient) GenerateAgent(ctx context.Context, req AgentRequest) (*AgentResponse, error) {
	maxTok := c.maxOut
	if maxTok <= 0 {
		maxTok = 1024
	}

	// Extract system prompt from the first system message, if any.
	var rawSystemPrompt string
	msgs := make([]anthropicMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			rawSystemPrompt = m.Content
		case "tool":
			// Anthropic expects tool results as user messages with typed blocks.
			block := anthropicBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			}
			msgs = append(msgs, anthropicMessage{Role: "user", Content: []anthropicBlock{block}})
		case "assistant":
			if len(m.ToolCalls) > 0 {
				var blocks []anthropicBlock
				if m.Content != "" {
					blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
				}
				for _, tc := range m.ToolCalls {
					var input any
					if err := json.Unmarshal([]byte(tc.Args), &input); err != nil {
						input = tc.Args
					}
					blocks = append(blocks, anthropicBlock{
						Type:  "tool_use",
						ID:    tc.ID,
						Name:  tc.Name,
						Input: input,
					})
				}
				msgs = append(msgs, anthropicMessage{Role: "assistant", Content: blocks})
			} else {
				msgs = append(msgs, anthropicMessage{Role: "assistant", Content: m.Content})
			}
		default:
			msgs = append(msgs, anthropicMessage{Role: m.Role, Content: m.Content})
		}
	}

	var tools []anthropicTool
	for _, td := range req.Tools {
		tools = append(tools, anthropicTool{
			Name:        td.Name,
			Description: td.Description,
			InputSchema: td.Parameters,
		})
	}

	var systemField any = rawSystemPrompt
	if c.promptCaching && rawSystemPrompt != "" {
		systemField = []anthropicSystemBlock{
			{Type: "text", Text: rawSystemPrompt, CacheControl: &anthropicCacheControl{Type: "ephemeral"}},
		}
	}

	payload := anthropicRequest{
		Model:     c.model,
		System:    systemField,
		Messages:  msgs,
		MaxTokens: maxTok,
		Tools:     tools,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	if c.promptCaching {
		httpReq.Header.Set("anthropic-beta", anthropicCachingBeta)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, apiStatusError("anthropic", resp, respBody)
	}

	var decoded anthropicResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if decoded.Error != nil {
		return nil, apiResponseError("anthropic", 0, decoded.Error.Type, decoded.Error.Message)
	}

	ar := &AgentResponse{}
	for _, block := range decoded.Content {
		switch block.Type {
		case "text":
			ar.Content += block.Text
		case "tool_use":
			ar.ToolCalls = append(ar.ToolCalls, ToolCall{
				ID:   block.ID,
				Name: block.Name,
				Args: string(block.Input),
			})
		}
	}
	return ar, nil
}
