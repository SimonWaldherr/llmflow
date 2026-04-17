package prompt

import (
	"bytes"
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
	return body.String(), nil
}

// FormatInstructions generates a prompt instruction string based on the
// processing config. The returned string should be appended to the user
// prompt so the LLM knows exactly how to format its response.
//
// Rules:
//   - Returns "" when ResponseFormat is empty or "text" AND Thinking is false.
//   - When Thinking is true, the LLM is asked to reason inside <thinking>…</thinking>
//     before producing the final output.
//   - When ResponseFormat is "json", "xml", or "csv", the LLM is asked to emit
//     a compact JSON object (conversion to XML/CSV happens in the output layer).
//   - When ResponseSchema is non-empty the instruction lists every expected field
//     with its type hint; otherwise only the format is enforced.
func FormatInstructions(cfg config.ProcessingConfig) string {
	format := strings.ToLower(strings.TrimSpace(cfg.ResponseFormat))
	wantStructured := format == "json" || format == "xml" || format == "csv"

	if !wantStructured && !cfg.Thinking {
		return ""
	}

	var b strings.Builder

	if cfg.Thinking {
		b.WriteString("First, reason through the problem step by step inside a <thinking>…</thinking> block. " +
			"After the closing </thinking> tag, ")
		if wantStructured {
			b.WriteString("output ONLY")
		} else {
			b.WriteString("write your final answer.")
			return b.String()
		}
	} else {
		b.WriteString("Output ONLY")
	}

	b.WriteString(" a compact JSON object")

	if len(cfg.ResponseSchema) > 0 {
		b.WriteString(" with exactly these fields:\n")
		keys := make([]string, 0, len(cfg.ResponseSchema))
		for k := range cfg.ResponseSchema {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(fmt.Sprintf("  - %s: %s\n", k, cfg.ResponseSchema[k]))
		}
		b.WriteString("Do not include any keys not listed above.")
	} else {
		b.WriteString(". Do not include any other text outside the JSON object.")
	}

	return b.String()
}

func (b *Builder) Build(record map[string]any) (string, error) {
	var body bytes.Buffer
	data := map[string]any{"record": record}
	for k, v := range record {
		data[k] = v
	}
	if err := b.tpl.Execute(&body, data); err != nil {
		return "", fmt.Errorf("render input template: %w", err)
	}

	var out bytes.Buffer
	if b.cfg.PrePrompt != "" {
		out.WriteString(b.cfg.PrePrompt)
		out.WriteString("\n\n")
	}
	out.Write(body.Bytes())
	if b.cfg.PostPrompt != "" {
		out.WriteString("\n\n")
		out.WriteString(b.cfg.PostPrompt)
	}
	return out.String(), nil
}
