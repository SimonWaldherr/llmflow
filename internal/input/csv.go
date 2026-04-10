package input

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type CSVReader struct {
	f   *os.File
	cfg config.InputConfig
}

func NewCSVReader(cfg config.InputConfig) (*CSVReader, error) {
	f, err := os.Open(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("open csv: %w", err)
	}
	return &CSVReader{f: f, cfg: cfg}, nil
}

func (r *CSVReader) ReadAll(ctx context.Context) ([]Record, error) {
	cr := csv.NewReader(r.f)
	delim, _ := utf8.DecodeRuneInString(r.cfg.CSV.Delimiter)
	if delim != utf8.RuneError {
		cr.Comma = delim
	}

	rows, err := cr.ReadAll()
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read csv: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	var headers []string
	start := 0
	if r.cfg.CSV.HasHeader {
		headers = rows[0]
		start = 1
	} else {
		for i := range rows[0] {
			headers = append(headers, fmt.Sprintf("col_%d", i+1))
		}
	}

	result := make([]Record, 0, len(rows)-start)
	for i := start; i < len(rows); i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		rec := make(Record, len(headers))
		for j, h := range headers {
			if j < len(rows[i]) {
				rec[h] = rows[i][j]
			} else {
				rec[h] = ""
			}
		}
		result = append(result, rec)
	}
	return result, nil
}

func (r *CSVReader) Close() error { return r.f.Close() }
