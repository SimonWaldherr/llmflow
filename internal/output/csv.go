package output

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"unicode/utf8"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type CSVWriter struct {
	f        *os.File
	cfg      config.OutputConfig
	cw       *csv.Writer
	headers  []string
	prepared bool
}

func NewCSVWriter(cfg config.OutputConfig) (*CSVWriter, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o750); err != nil {
		return nil, fmt.Errorf("create csv output dir: %w", err)
	}
	f, err := os.Create(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("create csv output: %w", err)
	}
	return &CSVWriter{f: f, cfg: cfg}, nil
}

func (w *CSVWriter) Prepare(_ context.Context, columns []string) error {
	if w.prepared {
		return nil
	}
	w.headers = append([]string(nil), columns...)
	w.cw = csv.NewWriter(w.f)
	delim, _ := utf8.DecodeRuneInString(w.cfg.CSV.Delimiter)
	if delim != utf8.RuneError {
		w.cw.Comma = delim
	}
	if len(w.headers) > 0 {
		if err := w.cw.Write(w.headers); err != nil {
			return fmt.Errorf("write csv header: %w", err)
		}
		w.cw.Flush()
		if err := w.cw.Error(); err != nil {
			return fmt.Errorf("flush csv header: %w", err)
		}
		if err := w.f.Sync(); err != nil {
			return fmt.Errorf("sync csv header: %w", err)
		}
	}
	w.prepared = true
	return nil
}

func (w *CSVWriter) WriteRecord(ctx context.Context, record Record) error {
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
	row := make([]string, len(w.headers))
	for i, h := range w.headers {
		row[i] = fmt.Sprint(record[h])
	}
	if err := w.cw.Write(row); err != nil {
		return fmt.Errorf("write csv row: %w", err)
	}
	w.cw.Flush()
	if err := w.cw.Error(); err != nil {
		return fmt.Errorf("flush csv row: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("sync csv row: %w", err)
	}
	return nil
}

func (w *CSVWriter) WriteAll(ctx context.Context, records []Record) error {
	if err := w.Prepare(ctx, unionHeaders(records)); err != nil {
		return err
	}
	for _, rec := range records {
		if err := w.WriteRecord(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

func (w *CSVWriter) Close() error {
	if w.cw != nil {
		w.cw.Flush()
		if err := w.cw.Error(); err != nil {
			return err
		}
	}
	return w.f.Close()
}

func unionHeaders(records []Record) []string {
	seen := map[string]struct{}{}
	for _, rec := range records {
		for k := range rec {
			seen[k] = struct{}{}
		}
	}
	headers := make([]string, 0, len(seen))
	for k := range seen {
		headers = append(headers, k)
	}
	sort.Strings(headers)
	return headers
}
