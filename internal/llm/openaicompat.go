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
	"net/url"
	"strings"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type openAICompatClient struct {
	httpClient    *http.Client
	baseURL       string
	apiKey        string
	model         string
	provider      string
	apiVersion    string
	maxOut        int64
	promptCaching bool
}

func newOpenAICompatClient(cfg config.APIConfig, apiKey string) *openAICompatClient {
	return &openAICompatClient{
		httpClient:    &http.Client{Timeout: cfg.Timeout},
		baseURL:       strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:        apiKey,
		model:         cfg.Model,
		provider:      strings.ToLower(cfg.Provider),
		apiVersion:    strings.TrimSpace(cfg.APIVersion),
		maxOut:        cfg.MaxOutputTokens,
		promptCaching: cfg.PromptCaching,
	}
}

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

type chatRequest struct {
	Model     string        `json:"model,omitempty"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int64         `json:"max_tokens,omitempty"`
	Tools     []oaiTool     `json:"tools,omitempty"`
}

type chatMessage struct {
	Role       string        `json:"role"`
	Content    any           `json:"content"` // string or null
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

type oaiTool struct {
	Type     string      `json:"type"` // "function"
	Function oaiFunction `json:"function"`
}

type oaiFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type oaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   *string       `json:"content"`
			ToolCalls []oaiToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// Generator (simple, single-turn)
// ---------------------------------------------------------------------------

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
	if c.isAzure() {
		payload.Model = ""
	}
	return c.doChat(ctx, payload)
}

// ---------------------------------------------------------------------------
// AgentGenerator (multi-turn with tool calling)
// ---------------------------------------------------------------------------

func (c *openAICompatClient) GenerateAgent(ctx context.Context, req AgentRequest) (*AgentResponse, error) {
	msgs := make([]chatMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		cm := chatMessage{Role: m.Role}
		switch m.Role {
		case "assistant":
			cm.Content = m.Content
			for _, tc := range m.ToolCalls {
				oc := oaiToolCall{ID: tc.ID, Type: "function"}
				oc.Function.Name = tc.Name
				oc.Function.Arguments = tc.Args
				cm.ToolCalls = append(cm.ToolCalls, oc)
			}
		case "tool":
			cm.Content = m.Content
			cm.ToolCallID = m.ToolCallID
			cm.Name = m.ToolName
		default:
			cm.Content = m.Content
		}
		msgs = append(msgs, cm)
	}

	var tools []oaiTool
	for _, td := range req.Tools {
		tools = append(tools, oaiTool{
			Type: "function",
			Function: oaiFunction{
				Name:        td.Name,
				Description: td.Description,
				Parameters:  td.Parameters,
			},
		})
	}

	payload := chatRequest{
		Model:     c.model,
		Messages:  msgs,
		MaxTokens: c.maxOut,
		Tools:     tools,
	}
	if c.isAzure() {
		payload.Model = ""
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.chatCompletionsURL(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(httpReq)

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
		return nil, fmt.Errorf("api status %d: %s", resp.StatusCode, string(respBody))
	}

	var decoded chatResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if decoded.Error != nil {
		return nil, fmt.Errorf("api error (%s): %s", decoded.Error.Type, decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("no choices returned by api")
	}

	choice := decoded.Choices[0]
	ar := &AgentResponse{}

	// Tool calls take precedence over content.
	if len(choice.Message.ToolCalls) > 0 {
		for _, tc := range choice.Message.ToolCalls {
			ar.ToolCalls = append(ar.ToolCalls, ToolCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Args: tc.Function.Arguments,
			})
		}
		return ar, nil
	}

	if choice.Message.Content != nil {
		ar.Content = *choice.Message.Content
	}
	return ar, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (c *openAICompatClient) doChat(ctx context.Context, payload chatRequest) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.chatCompletionsURL(), bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(req)
	// Enable explicit prompt caching beta header for providers that support it
	// (e.g. OpenAI). The static system-message prefix is automatically eligible
	// for caching when it is ≥ 1024 tokens; this header opts in to the feature.
	if c.promptCaching {
		req.Header.Set("OpenAI-Beta", "prompt-caching=1")
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
	if decoded.Choices[0].Message.Content == nil {
		return "", fmt.Errorf("no content in response")
	}
	return *decoded.Choices[0].Message.Content, nil
}

func (c *openAICompatClient) isAzure() bool {
	return c.provider == config.ProviderAzure
}

func (c *openAICompatClient) chatCompletionsURL() string {
	if !c.isAzure() {
		return c.baseURL + "/chat/completions"
	}

	base := strings.TrimRight(c.baseURL, "/")
	if !strings.Contains(base, "/openai/deployments/") && c.model != "" {
		base += "/openai/deployments/" + url.PathEscape(c.model)
	}
	endpoint := base + "/chat/completions"

	version := c.apiVersion
	if version == "" {
		version = "2024-10-21"
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		sep := "?"
		if strings.Contains(endpoint, "?") {
			sep = "&"
		}
		return endpoint + sep + "api-version=" + url.QueryEscape(version)
	}
	q := u.Query()
	if q.Get("api-version") == "" {
		q.Set("api-version", version)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *openAICompatClient) setAuthHeader(req *http.Request) {
	if c.apiKey == "" {
		return
	}
	if c.isAzure() {
		req.Header.Set("api-key", c.apiKey)
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
}
