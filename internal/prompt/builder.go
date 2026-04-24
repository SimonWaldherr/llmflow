package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/template"

	"github.com/SimonWaldherr/llmflow/internal/config"
	"github.com/SimonWaldherr/llmflow/internal/util"
)

type Builder struct {
	tpl *template.Template
	cfg config.PromptConfig
}

func New(cfg config.PromptConfig) (*Builder, error) {
	funcs := template.FuncMap{
		"toPrettyJSON": util.PrettyJSON,
	}
	tpl, err := template.New("input").Funcs(funcs).Parse(cfg.InputTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse prompt.input_template: %w", err)
	}
	return &Builder{tpl: tpl, cfg: cfg}, nil
}

func (b *Builder) SystemPrompt() string {
	return b.cfg.System
}

// PostPrompt returns the configured post-prompt text (used by batch mode).
func (b *Builder) PostPrompt() string {
	return b.cfg.PostPrompt
}

// BuildRaw renders only the input_template for the given record, without
// pre/post prompts. Used by batch mode to render each record individually
// before combining them.
func (b *Builder) BuildRaw(record map[string]any) (string, error) {
	var body bytes.Buffer
	data := map[string]any{"record": record}
	for k, v := range record {
		data[k] = v
	}
	if err := b.tpl.Execute(&body, data); err != nil {
		return "", fmt.Errorf("render input template: %w", err)
	}
	return normalizeInputPayload(body.String(), record), nil
}

// FormatInstructions generates a prompt instruction string based on the
// processing config. The returned string should be appended to the user
// prompt so the LLM knows exactly how to format its response.
//
// Rules:
//   - When Thinking is true, the LLM is first asked to reason inside a
//     <thinking>...</thinking> block (Step 1), then emit the JSON object (Step 2).
//   - The LLM is always required to emit a single JSON object — no markdown,
//     no code fences, no surrounding text.
//   - When ResponseSchema is non-empty the instruction lists every expected field
//     with a human-readable type description and provides a concrete example JSON
//     so the model has an unambiguous template to follow.
func FormatInstructions(cfg config.ProcessingConfig) string {
	schema := cfg.EffectiveLLMResponseSchema()

	var b strings.Builder

	if cfg.Thinking {
		b.WriteString("Step 1 — Reason: think through the task step by step inside a <thinking>...</thinking> block.\n")
		b.WriteString("Step 2 — Answer: immediately after the closing </thinking> tag output your final answer.\n\n")
		b.WriteString("The final answer (after </thinking>) must be ONLY a single JSON object.")
	} else {
		b.WriteString("Output ONLY a single JSON object.")
	}
	b.WriteString(" No markdown. No code fences (```). No text before or after the JSON.\n")

	if len(schema) == 0 {
		return b.String()
	}

	keys := schemaKeys(schema)

	b.WriteString("\nThe JSON object must contain exactly these fields — no more, no fewer:\n")
	for _, k := range keys {
		b.WriteString(fmt.Sprintf("  - \"%s\": %s\n", k, describeSchemaHint(schema[k])))
	}

	if example := buildExampleJSON(schema, keys); example != "" {
		b.WriteString("\nExpected format (replace placeholder values with actual values):\n")
		b.WriteString(example)
		b.WriteString("\n")
	}

	return b.String()
}

// FormatSystemNote returns a brief note to add to the system prompt when structured
// output is required. Injecting this at the system-prompt level creates a second
// layer of format enforcement that significantly improves LLM compliance, especially
// for providers that weight system instructions more heavily than user-turn instructions.
//
// Returns "" for plain-text/unstructured configs — only fires when the user has
// explicitly requested structured output (response_format, response_schema, parse_json,
// or thinking mode).
func FormatSystemNote(cfg config.ProcessingConfig) string {
	// Only add reinforcement for explicitly structured configs.
	// EffectiveResponseSchema (without fallback) is empty for plain-text jobs.
	hasExplicitSchema := len(cfg.EffectiveResponseSchema()) > 0
	hasStructuredMode := cfg.ResponseFormat != "" || cfg.ParseJSONResponse || hasExplicitSchema
	if !hasStructuredMode && !cfg.Thinking {
		return ""
	}
	if cfg.Thinking {
		return "You must always reason inside a <thinking>...</thinking> block first, " +
			"then respond with a single valid JSON object. " +
			"Never include any text, markdown, or code blocks outside those boundaries."
	}
	return "You must always respond with a single valid JSON object. " +
		"Never include text, markdown, or code blocks before or after the JSON."
}

// RepairPrompt builds the prompt used to ask the LLM to rewrite a malformed
// structured response into a valid JSON object. The returned string is intended
// as the user message in a one-shot repair call with a strict-formatter system prompt.
func RepairPrompt(schema map[string]string, raw string) string {
	var b strings.Builder
	b.WriteString("Convert the text below into a single valid JSON object. " +
		"Output ONLY the JSON — no markdown, no code fences, no explanations.\n")

	if len(schema) > 0 {
		keys := schemaKeys(schema)
		b.WriteString("\nRequired fields:\n")
		for _, k := range keys {
			b.WriteString(fmt.Sprintf("  - \"%s\": %s\n", k, describeSchemaHint(schema[k])))
		}
		if example := buildExampleJSON(schema, keys); example != "" {
			b.WriteString("\nExpected format:\n")
			b.WriteString(example)
			b.WriteString("\n")
		}
	}

	b.WriteString("\nText to convert:\n")
	b.WriteString(raw)
	return b.String()
}

// schemaKeys returns the keys of schema in deterministic sorted order.
func schemaKeys(schema map[string]string) []string {
	keys := make([]string, 0, len(schema))
	for k := range schema {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// describeSchemaHint converts a raw type hint string into a plain-English
// description suitable for inclusion in a prompt instruction.
func describeSchemaHint(hint string) string {
	trimmed := strings.TrimSpace(hint)
	if trimmed == "" {
		return "string"
	}

	lower := strings.ToLower(trimmed)

	// Pipe-separated enum: A|B|C
	if strings.Contains(trimmed, "|") {
		parts := strings.Split(trimmed, "|")
		quoted := make([]string, 0, len(parts))
		for _, p := range parts {
			v := strings.TrimSpace(p)
			if v != "" {
				quoted = append(quoted, `"`+v+`"`)
			}
		}
		if len(quoted) >= 2 {
			return `string — exactly one of: ` + strings.Join(quoted, ", ")
		}
	}

	// "one of: X, Y, Z" patterns
	if idx := strings.Index(lower, "one of"); idx >= 0 {
		rest := strings.TrimSpace(trimmed[idx+len("one of"):])
		rest = strings.TrimLeft(rest, ": ")
		if rest != "" {
			sep := ","
			if !strings.Contains(rest, ",") && strings.Contains(rest, "|") {
				sep = "|"
			}
			parts := strings.Split(rest, sep)
			quoted := make([]string, 0, len(parts))
			for _, p := range parts {
				v := strings.TrimSpace(p)
				if v != "" {
					quoted = append(quoted, `"`+v+`"`)
				}
			}
			if len(quoted) >= 2 {
				return `string — exactly one of: ` + strings.Join(quoted, ", ")
			}
		}
	}

	// Scalar types
	tokens := strings.FieldsFunc(lower, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	has := func(t string) bool {
		for _, tok := range tokens {
			if tok == t {
				return true
			}
		}
		return false
	}
	switch {
	case has("bool") || has("boolean"):
		return "boolean (true or false)"
	case has("int") || has("integer") || has("int32") || has("int64"):
		return "integer"
	case has("float") || has("double") || has("decimal") || has("number"):
		return "number"
	case has("string") || has("str") || has("text"):
		return "string"
	case has("array") || has("list") || has("slice"):
		return "array"
	case has("object") || has("map") || has("json"):
		return "object"
	default:
		// Free-text hint — keep the original but clarify it is a string field.
		return `string — ` + trimmed
	}
}

// buildExampleJSON constructs a one-line JSON object demonstrating the expected
// response shape, using placeholder values derived from each field's type hint.
// keys must be pre-sorted for deterministic output.
func buildExampleJSON(schema map[string]string, keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := exampleValueJSON(schema[k])
		parts = append(parts, fmt.Sprintf("%q:%s", k, v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// exampleValueJSON returns a JSON-encoded placeholder value for a schema hint.
func exampleValueJSON(hint string) string {
	trimmed := strings.TrimSpace(hint)
	lower := strings.ToLower(trimmed)

	// Pipe-separated or "one of" enum
	if strings.Contains(trimmed, "|") {
		parts := strings.Split(trimmed, "|")
		if first := strings.TrimSpace(parts[0]); first != "" {
			return `"` + first + `"`
		}
	}
	if idx := strings.Index(lower, "one of"); idx >= 0 {
		rest := strings.TrimSpace(trimmed[idx+len("one of"):])
		rest = strings.TrimLeft(rest, ": ")
		sep := ","
		if !strings.Contains(rest, ",") && strings.Contains(rest, "|") {
			sep = "|"
		}
		parts := strings.Split(rest, sep)
		if len(parts) > 0 {
			if first := strings.TrimSpace(parts[0]); first != "" {
				return `"` + first + `"`
			}
		}
	}

	tokens := strings.FieldsFunc(lower, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	has := func(t string) bool {
		for _, tok := range tokens {
			if tok == t {
				return true
			}
		}
		return false
	}
	switch {
	case has("bool") || has("boolean"):
		return "true"
	case has("int") || has("integer") || has("int32") || has("int64"):
		return "0"
	case has("float") || has("double") || has("decimal") || has("number"):
		return "0.0"
	case has("array") || has("list") || has("slice"):
		return "[]"
	case has("object") || has("map") || has("json"):
		return "{}"
	default:
		return `"<value>"`
	}
}

func (b *Builder) Build(record map[string]any) (string, error) {
	body, err := b.BuildRaw(record)
	if err != nil {
		return "", err
	}

	var out bytes.Buffer
	if b.cfg.PrePrompt != "" {
		out.WriteString(b.cfg.PrePrompt)
		out.WriteString("\n\n")
	}
	out.WriteString(body)
	if b.cfg.PostPrompt != "" {
		out.WriteString("\n\n")
		out.WriteString(b.cfg.PostPrompt)
	}
	return out.String(), nil
}

func normalizeInputPayload(rendered string, record map[string]any) string {
	trimmed := strings.TrimSpace(rendered)
	if trimmed == "" {
		return util.PrettyJSON(record)
	}

	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
		switch parsed.(type) {
		case map[string]any, []any:
			return util.PrettyJSON(parsed)
		default:
			// Scalars are valid JSON but we keep the transport payload as an object.
			return util.PrettyJSON(map[string]any{"input": parsed})
		}
	}

	// Non-JSON template output is wrapped into a JSON object so record transport
	// to the model remains JSON-only.
	return util.PrettyJSON(map[string]any{"input": trimmed})
}
