package output

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type JSONLWriter struct {
	f   *os.File
	enc *json.Encoder
}

func NewJSONLWriter(cfg config.OutputConfig) (*JSONLWriter, error) {
	f, err := os.Create(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("create jsonl output: %w", err)
	}
	return &JSONLWriter{f: f, enc: json.NewEncoder(f)}, nil
}

func (w *JSONLWriter) Prepare(_ context.Context, _ []string) error {
	if w.enc == nil {
		w.enc = json.NewEncoder(w.f)
	}
	return nil
}

func (w *JSONLWriter) WriteRecord(ctx context.Context, record Record) error {
	if err := w.Prepare(ctx, nil); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if err := w.enc.Encode(record); err != nil {
		return fmt.Errorf("write jsonl record: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("sync jsonl record: %w", err)
	}
	return nil
}

func (w *JSONLWriter) WriteAll(ctx context.Context, records []Record) error {
	if err := w.Prepare(ctx, nil); err != nil {
		return err
	}
	for _, rec := range records {
		if err := w.WriteRecord(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

func (w *JSONLWriter) Close() error { return w.f.Close() }
