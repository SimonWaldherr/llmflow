package output

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type JSONLWriter struct{ f *os.File }

func NewJSONLWriter(cfg config.OutputConfig) (*JSONLWriter, error) {
	f, err := os.Create(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("create jsonl output: %w", err)
	}
	return &JSONLWriter{f: f}, nil
}

func (w *JSONLWriter) WriteAll(ctx context.Context, records []Record) error {
	enc := json.NewEncoder(w.f)
	for _, rec := range records {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := enc.Encode(rec); err != nil {
			return fmt.Errorf("write jsonl record: %w", err)
		}
	}
	return nil
}

func (w *JSONLWriter) Close() error { return w.f.Close() }
