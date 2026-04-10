package prompt

import (
	"bytes"
	"fmt"
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
