package output

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"unicode/utf8"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type CSVWriter struct {
	f        *os.File
	cfg      config.OutputConfig
	cw       *csv.Writer
	headers  []string
	records  []Record
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
		if err := w.Prepare(ctx, sortedRecordKeys(record)); err != nil {
			return err
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	rec := cloneRecord(record)
	nextHeaders, changed := mergeHeaders(w.headers, rec)
	w.records = append(w.records, rec)
	if changed {
		w.headers = nextHeaders
		return w.rewriteAll()
	}
	return w.writeRow(rec)
}

func (w *CSVWriter) writeRow(record Record) error {
	row := make([]string, len(w.headers))
	for i, h := range w.headers {
		row[i] = stringifyCell(record[h])
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

func (w *CSVWriter) rewriteAll() error {
	if _, err := w.f.Seek(0, 0); err != nil {
		return fmt.Errorf("seek csv output: %w", err)
	}
	if err := w.f.Truncate(0); err != nil {
		return fmt.Errorf("truncate csv output: %w", err)
	}
	w.cw = csv.NewWriter(w.f)
	delim, _ := utf8.DecodeRuneInString(w.cfg.CSV.Delimiter)
	if delim != utf8.RuneError {
		w.cw.Comma = delim
	}
	if len(w.headers) > 0 {
		if err := w.cw.Write(w.headers); err != nil {
			return fmt.Errorf("rewrite csv header: %w", err)
		}
	}
	for _, rec := range w.records {
		row := make([]string, len(w.headers))
		for i, h := range w.headers {
			row[i] = stringifyCell(rec[h])
		}
		if err := w.cw.Write(row); err != nil {
			return fmt.Errorf("rewrite csv row: %w", err)
		}
	}
	w.cw.Flush()
	if err := w.cw.Error(); err != nil {
		return fmt.Errorf("flush rewritten csv: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("sync rewritten csv: %w", err)
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
	return slices.Sorted(maps.Keys(seen))
}

func sortedRecordKeys(record Record) []string {
	return slices.Sorted(maps.Keys(record))
}

func mergeHeaders(headers []string, record Record) ([]string, bool) {
	seen := make(map[string]struct{}, len(headers)+len(record))
	for _, h := range headers {
		seen[h] = struct{}{}
	}
	var missing []string
	for k := range record {
		if _, ok := seen[k]; ok {
			continue
		}
		missing = append(missing, k)
	}
	if len(missing) == 0 {
		return headers, false
	}
	slices.Sort(missing)
	next := append(append([]string(nil), headers...), missing...)
	return next, true
}

func cloneRecord(record Record) Record {
	out := make(Record, len(record))
	for k, v := range record {
		out[k] = v
	}
	return out
}

func stringifyCell(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case json.Number:
		return x.String()
	case bool:
		return fmt.Sprint(x)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return fmt.Sprint(x)
	default:
		b, err := json.Marshal(x)
		if err == nil {
			return string(b)
		}
		return fmt.Sprint(x)
	}
}
