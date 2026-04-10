package input

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type JSONReader struct {
	f   *os.File
	cfg config.InputConfig
}

func NewJSONReader(cfg config.InputConfig) (*JSONReader, error) {
	f, err := os.Open(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("open json input: %w", err)
	}
	return &JSONReader{f: f, cfg: cfg}, nil
}

func (r *JSONReader) ReadAll(ctx context.Context) ([]Record, error) {
	if r.cfg.Type == "jsonl" || r.cfg.JSON.JSONL {
		return r.readJSONL(ctx)
	}
	var payload any
	if err := json.NewDecoder(r.f).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	switch v := payload.(type) {
	case []any:
		out := make([]Record, 0, len(v))
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("json array element is not an object")
			}
			out = append(out, m)
		}
		return out, nil
	case map[string]any:
		return []Record{v}, nil
	default:
		return nil, fmt.Errorf("unsupported json top-level type %T", payload)
	}
}

func (r *JSONReader) readJSONL(ctx context.Context) ([]Record, error) {
	s := bufio.NewScanner(r.f)
	var out []Record
	for s.Scan() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		var rec Record
		if err := json.Unmarshal(s.Bytes(), &rec); err != nil {
			return nil, fmt.Errorf("decode jsonl line: %w", err)
		}
		out = append(out, rec)
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("scan jsonl: %w", err)
	}
	return out, nil
}

func (r *JSONReader) Close() error { return r.f.Close() }
