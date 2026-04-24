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

	if !strings.Contains(got, "<thinking>") {
		t.Fatalf("expected thinking block instruction, got: %q", got)
	}
	if !strings.Contains(got, "single JSON object") {
		t.Fatalf("expected strict single-object instruction, got: %q", got)
	}
	if !strings.Contains(got, `"A"`) {
		t.Fatalf("expected first enum value in example or description, got: %q", got)
	}
	if !strings.Contains(got, "classification") {
		t.Fatalf("expected field name in output, got: %q", got)
	}
}

func TestFormatInstructions_ThinkingTextOnly(t *testing.T) {
	got := FormatInstructions(config.ProcessingConfig{
		ResponseFormat: "text",
		Thinking:       true,
		ResponseField:  "answer",
	})

	if !strings.Contains(got, "Step 1") {
		t.Fatalf("expected step-by-step thinking instruction, got: %q", got)
	}
	if !strings.Contains(got, "single JSON object") {
		t.Fatalf("expected JSON contract instruction, got: %q", got)
	}
	if !strings.Contains(got, "answer") {
		t.Fatalf("expected response field name in schema, got: %q", got)
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

func TestFormatInstructions_ExampleJSON(t *testing.T) {
	got := FormatInstructions(config.ProcessingConfig{
		ResponseFormat: "json",
		ResponseSchema: map[string]string{
			"label":      "positive|negative",
			"confidence": "float",
			"approved":   "bool",
		},
	})

	// Should contain an example JSON
	if !strings.Contains(got, `"label"`) {
		t.Fatalf("expected field name in example JSON, got: %q", got)
	}
	if !strings.Contains(got, `"positive"`) {
		t.Fatalf("expected first enum value in example JSON, got: %q", got)
	}
	if !strings.Contains(got, "0.0") {
		t.Fatalf("expected float placeholder in example JSON, got: %q", got)
	}
	if !strings.Contains(got, "true") {
		t.Fatalf("expected bool placeholder in example JSON, got: %q", got)
	}
}

func TestFormatSystemNote_WithSchema(t *testing.T) {
	got := FormatSystemNote(config.ProcessingConfig{
		ResponseFormat: "json",
		ResponseSchema: map[string]string{"field": "string"},
	})
	if got == "" {
		t.Fatal("expected non-empty system note for structured output config")
	}
	if !strings.Contains(got, "JSON") {
		t.Fatalf("expected JSON mention in system note, got: %q", got)
	}
}

func TestFormatSystemNote_WithThinking(t *testing.T) {
	got := FormatSystemNote(config.ProcessingConfig{
		Thinking: true,
	})
	if !strings.Contains(got, "thinking") || !strings.Contains(got, "JSON") {
		t.Fatalf("expected thinking and JSON mention in system note, got: %q", got)
	}
}

func TestFormatSystemNote_NoStructure(t *testing.T) {
	got := FormatSystemNote(config.ProcessingConfig{})
	if got != "" {
		t.Fatalf("expected empty system note for unstructured config, got: %q", got)
	}
}

func TestRepairPrompt_WithSchema(t *testing.T) {
	schema := map[string]string{
		"label":  "A|B",
		"reason": "string",
	}
	got := RepairPrompt(schema, "I think it is A because reasons.")
	if !strings.Contains(got, "JSON") {
		t.Fatalf("expected JSON mention in repair prompt, got: %q", got)
	}
	if !strings.Contains(got, "label") {
		t.Fatalf("expected schema field in repair prompt, got: %q", got)
	}
	if !strings.Contains(got, "A because reasons") {
		t.Fatalf("expected original text in repair prompt, got: %q", got)
	}
}

func TestDescribeSchemaHint(t *testing.T) {
	cases := []struct{ hint, wantSubstr string }{
		{"A|B|C", `"A"`},
		{"one of: X, Y", `"X"`},
		{"bool", "boolean"},
		{"float", "number"},
		{"int", "integer"},
		{"string", "string"},
		{"array", "array"},
		{"object", "object"},
		{"short sentence", "short sentence"},
	}
	for _, c := range cases {
		got := describeSchemaHint(c.hint)
		if !strings.Contains(got, c.wantSubstr) {
			t.Errorf("describeSchemaHint(%q) = %q, want substring %q", c.hint, got, c.wantSubstr)
		}
	}
}

func TestBuildExampleJSON(t *testing.T) {
	schema := map[string]string{
		"flag":  "bool",
		"score": "float",
		"tag":   "X|Y",
	}
	keys := []string{"flag", "score", "tag"}
	got := buildExampleJSON(schema, keys)
	if got == "" {
		t.Fatal("expected non-empty example JSON")
	}
	if !strings.Contains(got, "true") {
		t.Errorf("expected bool true in example, got: %s", got)
	}
	if !strings.Contains(got, "0.0") {
		t.Errorf("expected float 0.0 in example, got: %s", got)
	}
	if !strings.Contains(got, `"X"`) {
		t.Errorf("expected first enum value in example, got: %s", got)
	}
}
