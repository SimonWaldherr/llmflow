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
	if !strings.Contains(result, "Name: Alice") {
		t.Fatalf("expected Name: Alice in %q", result)
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
	if !strings.HasPrefix(result, "Before.") || !strings.Contains(result, "Data: 42") || !strings.HasSuffix(result, "After.") {
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
