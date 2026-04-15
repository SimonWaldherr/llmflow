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
	f       *os.File
	cfg     config.InputConfig
	cr      *csv.Reader
	headers []string
	started bool
	ended   bool
}

func NewCSVReader(cfg config.InputConfig) (*CSVReader, error) {
	f, err := os.Open(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("open csv: %w", err)
	}
	return &CSVReader{f: f, cfg: cfg}, nil
}

func (r *CSVReader) init() {
	if r.cr != nil {
		return
	}
	r.cr = csv.NewReader(r.f)
	delim, _ := utf8.DecodeRuneInString(r.cfg.CSV.Delimiter)
	if delim != utf8.RuneError {
		r.cr.Comma = delim
	}
}

func (r *CSVReader) Next(ctx context.Context) (Record, error) {
	if r.ended {
		return nil, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.init()

	if !r.started {
		r.started = true
		if r.cfg.CSV.HasHeader {
			headers, err := r.cr.Read()
			if err != nil {
				if err == io.EOF {
					r.ended = true
					return nil, io.EOF
				}
				return nil, fmt.Errorf("read csv header: %w", err)
			}
			r.headers = append([]string(nil), headers...)
		} else {
			row, err := r.cr.Read()
			if err != nil {
				if err == io.EOF {
					r.ended = true
					return nil, io.EOF
				}
				return nil, fmt.Errorf("read csv row: %w", err)
			}
			r.headers = make([]string, len(row))
			for i := range row {
				r.headers[i] = fmt.Sprintf("col_%d", i+1)
			}
			rec := make(Record, len(r.headers))
			for i, h := range r.headers {
				rec[h] = row[i]
			}
			return rec, nil
		}
	}

	row, err := r.cr.Read()
	if err != nil {
		if err == io.EOF {
			r.ended = true
			return nil, io.EOF
		}
		return nil, fmt.Errorf("read csv row: %w", err)
	}
	rec := make(Record, len(r.headers))
	for i, h := range r.headers {
		if i < len(row) {
			rec[h] = row[i]
		} else {
			rec[h] = ""
		}
	}
	return rec, nil
}

func (r *CSVReader) ReadAll(ctx context.Context) ([]Record, error) {
	var out []Record
	for {
		rec, err := r.Next(ctx)
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return nil, err
		}
		out = append(out, rec)
	}
}

func (r *CSVReader) Close() error { return r.f.Close() }
