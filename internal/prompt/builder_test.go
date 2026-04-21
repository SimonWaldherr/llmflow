package prompt

import (
	"strings"
	"testing"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

func TestBuilderSystemPrompt(t *testing.T) {
	b, err := New(config.PromptConfig{
		System:        "You are a helper.",
		InputTemplate: "{{ .record }}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.SystemPrompt() != "You are a helper." {
		t.Fatalf("unexpected system prompt: %q", b.SystemPrompt())
	}
}

func TestBuilderBuildBasic(t *testing.T) {
	b, err := New(config.PromptConfig{InputTemplate: "Name: {{ .name }}"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := b.Build(map[string]any{"name": "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, `"input"`) || !strings.Contains(result, "Name: Alice") {
		t.Fatalf("expected wrapped JSON payload, got %q", result)
	}
}

func TestBuilderBuildWithPreAndPost(t *testing.T) {
	b, err := New(config.PromptConfig{
		PrePrompt:     "Before.",
		InputTemplate: "Data: {{ .val }}",
		PostPrompt:    "After.",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := b.Build(map[string]any{"val": "42"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result, "Before.") || !strings.Contains(result, `"input"`) || !strings.Contains(result, "Data: 42") || !strings.HasSuffix(result, "After.") {
		t.Fatalf("unexpected prompt: %q", result)
	}
}

func TestBuilderBuildPrettyJSON(t *testing.T) {
	b, err := New(config.PromptConfig{InputTemplate: "{{ toPrettyJSON .record }}"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := b.Build(map[string]any{"key": "value"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "\"key\"") || !strings.Contains(result, "\"value\"") {
		t.Fatalf("expected json output, got %q", result)
	}
}

func TestBuilderInvalidTemplate(t *testing.T) {
	if _, err := New(config.PromptConfig{InputTemplate: "{{ .unclosed"}); err == nil {
		t.Fatal("expected error for invalid template")
	}
}

func TestBuilderTemplateExecError(t *testing.T) {
	b, err := New(config.PromptConfig{InputTemplate: `{{ call .missing }}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Build(map[string]any{}); err == nil {
		t.Fatal("expected template execution error")
	}
}

func TestFormatInstructions_StrictStructuredWithThinking(t *testing.T) {
	got := FormatInstructions(config.ProcessingConfig{
		ResponseFormat: "json",
		Thinking:       true,
		ResponseSchema: map[string]string{
			"classification": "A|B|C",
		},
	})

	if !strings.Contains(got, "<thinking>...</thinking>") {
		t.Fatalf("expected thinking block instruction, got: %q", got)
	}
	if !strings.Contains(got, "one compact JSON object") {
		t.Fatalf("expected strict single-object instruction, got: %q", got)
	}
	if !strings.Contains(got, "A|B|C") {
		t.Fatalf("expected enum hint to be present, got: %q", got)
	}
}

func TestFormatInstructions_ThinkingTextOnly(t *testing.T) {
	got := FormatInstructions(config.ProcessingConfig{
		ResponseFormat: "text",
		Thinking:       true,
		ResponseField:  "answer",
	})

	if !strings.Contains(got, "Then output exactly one final answer and nothing else.") {
		t.Fatalf("unexpected thinking text instruction: %q", got)
	}
	if !strings.Contains(got, "one compact JSON object") || !strings.Contains(got, "answer") {
		t.Fatalf("expected JSON response contract with fallback schema, got: %q", got)
	}
}

func TestFormatInstructions_IncludesDebugField(t *testing.T) {
	got := FormatInstructions(config.ProcessingConfig{
		ResponseFormat: "json",
		ResponseSchema: map[string]string{
			"versandart": "Paket|Spedition",
		},
		DebugField:     "debug_reason",
		DebugFieldHint: "short reason",
	})

	if !strings.Contains(got, "debug_reason") {
		t.Fatalf("expected debug field in format instructions, got: %q", got)
	}
}
