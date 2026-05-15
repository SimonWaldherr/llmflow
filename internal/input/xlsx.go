package input

import (
	"context"
	"fmt"
	"io"

	"github.com/SimonWaldherr/llmflow/internal/config"
	"github.com/xuri/excelize/v2"
)

type XLSXReader struct {
	f       *excelize.File
	cfg     config.InputConfig
	rows    [][]string
	headers []string
	idx     int
	ended   bool
}

func NewXLSXReader(cfg config.InputConfig) (*XLSXReader, error) {
	f, err := excelize.OpenFile(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("open xlsx input: %w", err)
	}
	sheet := f.GetSheetName(f.GetActiveSheetIndex())
	if sheet == "" {
		sheets := f.GetSheetList()
		if len(sheets) == 0 {
			_ = f.Close()
			return nil, fmt.Errorf("xlsx input has no sheets")
		}
		sheet = sheets[0]
	}
	rows, err := f.GetRows(sheet)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("read xlsx rows: %w", err)
	}
	return &XLSXReader{f: f, cfg: cfg, rows: rows}, nil
}

func (r *XLSXReader) Next(ctx context.Context) (Record, error) {
	if r.ended {
		return nil, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for r.idx < len(r.rows) && isEmptyXLSXRow(r.rows[r.idx]) {
		r.idx++
	}
	if r.idx >= len(r.rows) {
		r.ended = true
		return nil, io.EOF
	}
	if r.headers == nil {
		first := r.rows[r.idx]
		if r.cfg.CSV.HasHeader {
			r.headers = append([]string(nil), first...)
			r.idx++
			return r.Next(ctx)
		}
		r.headers = make([]string, len(first))
		for i := range first {
			r.headers[i] = fmt.Sprintf("col_%d", i+1)
		}
	}
	row := r.rows[r.idx]
	r.idx++
	if len(row) > len(r.headers) {
		for i := len(r.headers); i < len(row); i++ {
			r.headers = append(r.headers, fmt.Sprintf("col_%d", i+1))
		}
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

func (r *XLSXReader) ReadAll(ctx context.Context) ([]Record, error) {
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

func (r *XLSXReader) Close() error { return r.f.Close() }

func isEmptyXLSXRow(row []string) bool {
	for _, cell := range row {
		if cell != "" {
			return false
		}
	}
	return true
}
