package output

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"sort"
	"unicode/utf8"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type CSVWriter struct {
	f   *os.File
	cfg config.OutputConfig
}

func NewCSVWriter(cfg config.OutputConfig) (*CSVWriter, error) {
	f, err := os.Create(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("create csv output: %w", err)
	}
	return &CSVWriter{f: f, cfg: cfg}, nil
}

func (w *CSVWriter) WriteAll(ctx context.Context, records []Record) error {
	cw := csv.NewWriter(w.f)
	delim, _ := utf8.DecodeRuneInString(w.cfg.CSV.Delimiter)
	if delim != utf8.RuneError {
		cw.Comma = delim
	}
	if len(records) == 0 {
		cw.Flush()
		return cw.Error()
	}
	headers := unionHeaders(records)
	if err := cw.Write(headers); err != nil {
		return fmt.Errorf("write csv header: %w", err)
	}
	for _, rec := range records {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		row := make([]string, len(headers))
		for i, h := range headers {
			row[i] = fmt.Sprint(rec[h])
		}
		if err := cw.Write(row); err != nil {
			return fmt.Errorf("write csv row: %w", err)
		}
	}
	cw.Flush()
	return cw.Error()
}

func (w *CSVWriter) Close() error { return w.f.Close() }

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
