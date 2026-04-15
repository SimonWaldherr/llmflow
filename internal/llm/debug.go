package llm

// debug.go provides a generator wrapper that logs every LLM request and
// response in detail when debug mode is enabled.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
)

// debuggingGenerator wraps any Generator (and optionally AgentGenerator) and
// logs full prompt/response details at slog.LevelDebug.
type debuggingGenerator struct {
	inner  Generator
	logger *slog.Logger
}

// NewDebuggingGenerator returns a Generator that transparently wraps inner and
// logs every request and response at DEBUG level using logger.
func NewDebuggingGenerator(inner Generator, logger *slog.Logger) Generator {
	return &debuggingGenerator{inner: inner, logger: logger}
}

func (d *debuggingGenerator) Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	d.logger.Debug("llm.request",
		"system_prompt_len", len(systemPrompt),
		"user_prompt_len", len(userPrompt),
		"system_prompt", systemPrompt,
		"user_prompt", userPrompt,
	)

	start := time.Now()
	resp, err := d.inner.Generate(ctx, systemPrompt, userPrompt)
	elapsed := time.Since(start)

	if err != nil {
		d.logger.Debug("llm.error",
			"error", err.Error(),
			"duration_ms", elapsed.Milliseconds(),
		)
		return resp, err
	}

	d.logger.Debug("llm.response",
		"response_len", len(resp),
		"duration_ms", elapsed.Milliseconds(),
		"response", resp,
	)
	return resp, nil
}

// GenerateAgent implements AgentGenerator when the wrapped generator supports it.
func (d *debuggingGenerator) GenerateAgent(ctx context.Context, req AgentRequest) (*AgentResponse, error) {
	ag, ok := d.inner.(AgentGenerator)
	if !ok {
		return nil, nil
	}

	// Log the full message chain.
	msgsJSON, _ := json.Marshal(req.Messages)
	toolNames := make([]string, 0, len(req.Tools))
	for _, t := range req.Tools {
		toolNames = append(toolNames, t.Name)
	}
	d.logger.Debug("llm.agent_request",
		"message_count", len(req.Messages),
		"tools", strings.Join(toolNames, ", "),
		"messages_json", string(msgsJSON),
	)

	start := time.Now()
	resp, err := ag.GenerateAgent(ctx, req)
	elapsed := time.Since(start)

	if err != nil {
		d.logger.Debug("llm.agent_error",
			"error", err.Error(),
			"duration_ms", elapsed.Milliseconds(),
		)
		return resp, err
	}

	respJSON, _ := json.Marshal(resp)
	d.logger.Debug("llm.agent_response",
		"has_tool_calls", len(resp.ToolCalls) > 0,
		"tool_call_count", len(resp.ToolCalls),
		"content_len", len(resp.Content),
		"duration_ms", elapsed.Milliseconds(),
		"response_json", string(respJSON),
	)
	return resp, nil
}
