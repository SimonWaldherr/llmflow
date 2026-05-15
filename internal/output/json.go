package output

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type JSONWriter struct {
	path     string
	records  []Record
	prepared bool
}

func NewJSONWriter(cfg config.OutputConfig) (*JSONWriter, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o750); err != nil {
		return nil, fmt.Errorf("create json output dir: %w", err)
	}
	return &JSONWriter{path: cfg.Path}, nil
}

func (w *JSONWriter) Prepare(_ context.Context, _ []string) error {
	w.prepared = true
	return nil
}

func (w *JSONWriter) WriteRecord(ctx context.Context, record Record) error {
	if !w.prepared {
		if err := w.Prepare(ctx, nil); err != nil {
			return err
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	w.records = append(w.records, cloneRecord(record))
	return nil
}

func (w *JSONWriter) WriteAll(ctx context.Context, records []Record) error {
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

func (w *JSONWriter) Close() error {
	records := w.records
	if records == nil {
		records = []Record{}
	}
	b, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json output: %w", err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(w.path, b, 0o640); err != nil {
		return fmt.Errorf("write json output: %w", err)
	}
	return nil
}
