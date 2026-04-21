package app

import (
	"context"
	"strings"
	"testing"

	"github.com/SimonWaldherr/llmflow/internal/config"
	"github.com/SimonWaldherr/llmflow/internal/input"
	"github.com/SimonWaldherr/llmflow/internal/prompt"
)

func boolPtr(v bool) *bool { return &v }

func TestParseJSONResponseFields_StrictThinkingAndSchema(t *testing.T) {
	cfg := config.ProcessingConfig{
		ResponseFormat: "json",
		Thinking:       true,
		ResponseSchema: map[string]string{
			"approved":       "boolean",
			"score":          "int",
			"classification": "A|B|C",
		},
	}
	resp := `<thinking>check constraints first</thinking>{"approved":true,"score":3,"classification":"B"}`

	parsed, err := parseJSONResponseFields(resp, cfg)
	if err != nil {
		t.Fatalf("expected strict thinking response to parse, got error: %v", err)
	}
	if v, ok := parsed["approved"].(bool); !ok || !v {
		t.Fatalf("expected approved=true, got %#v", parsed["approved"])
	}
	if !isIntegerValue(parsed["score"]) {
		t.Fatalf("expected integer score, got %#v", parsed["score"])
	}
	if v, ok := parsed["classification"].(string); !ok || v != "B" {
		t.Fatalf("expected classification=B, got %#v", parsed["classification"])
	}
}

func TestParseJSONResponseFields_StrictRejectsMixedText(t *testing.T) {
	cfg := config.ProcessingConfig{
		ResponseFormat: "json",
		ResponseSchema: map[string]string{"approved": "boolean"},
	}
	resp := `Here you go: {"approved":true}`

	_, err := parseJSONResponseFields(resp, cfg)
	if err == nil {
		t.Fatal("expected strict mode parse error for mixed text")
	}
}

func TestParseJSONResponseFields_SchemaRejectsEnumValue(t *testing.T) {
	cfg := config.ProcessingConfig{
		ResponseFormat: "json",
		ResponseSchema: map[string]string{"classification": "A|B|C"},
	}

	_, err := parseJSONResponseFields(`{"classification":"D"}`, cfg)
	if err == nil {
		t.Fatal("expected enum validation error")
	}
	if !strings.Contains(err.Error(), "one of") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseJSONResponseFields_LenientWhenStrictOutputDisabled(t *testing.T) {
	cfg := config.ProcessingConfig{
		ParseJSONResponse: true,
		StrictOutput:      boolPtr(false),
		ResponseSchema: map[string]string{
			"approved": "boolean",
		},
	}

	parsed, err := parseJSONResponseFields(`Result: {"approved":"yes"}`, cfg)
	if err != nil {
		t.Fatalf("expected lenient parsing without error, got: %v", err)
	}
	if v, ok := parsed["approved"].(string); !ok || v != "yes" {
		t.Fatalf("expected extracted approved=yes, got %#v", parsed["approved"])
	}
}

func TestProcessRecords_StrictStructuredOutputRejectsInvalid(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Processing.ResponseFormat = "json"
	cfg.Processing.ResponseSchema = map[string]string{"approved": "boolean"}
	cfg.Processing.ContinueOnError = false

	a := New(cfg, newTestLogger())
	gen := &fakeGenerator{response: `Answer: {"approved":true}`}
	pb := newTestPromptBuilder(t)

	_, err := a.processRecords(context.Background(), gen, pb, nil, []map[string]any{{"name": "Alice"}}, 1, nil)
	if err == nil {
		t.Fatal("expected strict structured output error")
	}
}

func TestProcessRecords_StrictStructuredOutputAcceptsThinking(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Processing.ResponseFormat = "json"
	cfg.Processing.Thinking = true
	cfg.Processing.ResponseSchema = map[string]string{
		"approved":       "boolean",
		"score":          "int",
		"classification": "A|B|C",
	}

	a := New(cfg, newTestLogger())
	gen := &fakeGenerator{response: `<thinking>evaluate criteria</thinking>{"approved":true,"score":2,"classification":"A"}`}
	pb := newTestPromptBuilder(t)

	results, err := a.processRecords(context.Background(), gen, pb, nil, []map[string]any{{"name": "Alice"}}, 1, nil)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if v, ok := results[0]["approved"].(bool); !ok || !v {
		t.Fatalf("expected approved=true, got %#v", results[0]["approved"])
	}
	if !isIntegerValue(results[0]["score"]) {
		t.Fatalf("expected integer score, got %#v", results[0]["score"])
	}
}

func TestProcessRecords_LenientStructuredOutputWhenStrictDisabled(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Processing.ResponseFormat = "json"
	cfg.Processing.StrictOutput = boolPtr(false)
	cfg.Processing.ResponseSchema = map[string]string{"approved": "boolean"}

	a := New(cfg, newTestLogger())
	gen := &fakeGenerator{response: `Answer: {"approved":"yes"}`}
	pb := newTestPromptBuilder(t)

	results, err := a.processRecords(context.Background(), gen, pb, nil, []map[string]any{{"name": "Alice"}}, 1, nil)
	if err != nil {
		t.Fatalf("expected lenient success, got error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if v, ok := results[0]["approved"].(string); !ok || v != "yes" {
		t.Fatalf("expected approved=yes from lenient extraction, got %#v", results[0]["approved"])
	}
}

func TestProcessBatch_StrictStructuredRejectsStringItems(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Processing.ResponseFormat = "json"
	cfg.Processing.ResponseSchema = map[string]string{"approved": "boolean"}

	a := New(cfg, newTestLogger())
	gen := &fakeGenerator{response: `["still text"]`}
	pb, err := prompt.New(config.PromptConfig{InputTemplate: "{{ .name }}"})
	if err != nil {
		t.Fatalf("prompt init failed: %v", err)
	}

	_, err = a.processBatch(context.Background(), gen, pb, 0, []input.Record{{"name": "Alice"}})
	if err == nil {
		t.Fatal("expected strict batch error for non-object item")
	}
}

func TestProcessBatch_LenientStructuredAllowsStringItemsWhenStrictDisabled(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Processing.ResponseFormat = "json"
	cfg.Processing.StrictOutput = boolPtr(false)
	cfg.Processing.ResponseSchema = map[string]string{"approved": "boolean"}

	a := New(cfg, newTestLogger())
	gen := &fakeGenerator{response: `["still text"]`}
	pb, err := prompt.New(config.PromptConfig{InputTemplate: "{{ .name }}"})
	if err != nil {
		t.Fatalf("prompt init failed: %v", err)
	}

	out, err := a.processBatch(context.Background(), gen, pb, 0, []input.Record{{"name": "Alice"}})
	if err != nil {
		t.Fatalf("expected lenient batch success, got error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected one output record, got %d", len(out))
	}
	if v, ok := out[0][cfg.Processing.ResponseField].(string); !ok || v != "still text" {
		t.Fatalf("unexpected response field value: %#v", out[0][cfg.Processing.ResponseField])
	}
}
