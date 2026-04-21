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
//   - Returns "" when ResponseFormat is empty or "text" AND Thinking is false.
//   - When Thinking is true, the LLM is asked to reason inside <thinking>...</thinking>
//     before producing the final output.
//   - When ResponseFormat is "json", "xml", or "csv", the LLM is asked to emit
//     a compact JSON object (conversion to XML/CSV happens in the output layer).
//   - When ResponseSchema is non-empty the instruction lists every expected field
//     with its type hint; otherwise only the format is enforced.
func FormatInstructions(cfg config.ProcessingConfig) string {
	schema := cfg.EffectiveLLMResponseSchema()

	var b strings.Builder

	if cfg.Thinking {
		b.WriteString("Reason through the task. If you expose reasoning, put it inside a <thinking>...</thinking> block. " +
			"Then output exactly one final answer and nothing else. ")
		b.WriteString("The final answer must be")
	} else {
		b.WriteString("Output exactly")
	}

	b.WriteString(" one compact JSON object")

	if len(schema) > 0 {
		b.WriteString(" with exactly these fields and types:\n")
		keys := make([]string, 0, len(schema))
		for k := range schema {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(fmt.Sprintf("  - %s: %s\n", k, schema[k]))
		}
		b.WriteString("Do not include any keys not listed above. " +
			"For enum hints like A|B|C, return exactly one listed value.")
	} else {
		b.WriteString(". Do not include any text before or after the JSON object.")
	}

	return b.String()
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
