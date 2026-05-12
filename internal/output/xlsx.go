package output

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/SimonWaldherr/llmflow/internal/config"
	"github.com/xuri/excelize/v2"
)

type XLSXWriter struct {
	path     string
	headers  []string
	records  []Record
	prepared bool
}

func NewXLSXWriter(cfg config.OutputConfig) (*XLSXWriter, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o750); err != nil {
		return nil, fmt.Errorf("create xlsx output dir: %w", err)
	}
	return &XLSXWriter{path: cfg.Path}, nil
}

func (w *XLSXWriter) Prepare(_ context.Context, columns []string) error {
	if w.prepared {
		return nil
	}
	w.headers = append([]string(nil), columns...)
	w.prepared = true
	return nil
}

func (w *XLSXWriter) WriteRecord(ctx context.Context, record Record) error {
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
	w.headers, _ = mergeHeaders(w.headers, rec)
	w.records = append(w.records, rec)
	return nil
}

func (w *XLSXWriter) WriteAll(ctx context.Context, records []Record) error {
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

func (w *XLSXWriter) Close() error {
	if w.path == "" {
		return nil
	}
	f := excelize.NewFile()
	const sheetName = "Output"
	defaultSheet := f.GetSheetName(f.GetActiveSheetIndex())
	if defaultSheet == "" {
		defaultSheet = "Sheet1"
	}
	if defaultSheet != sheetName {
		if err := f.SetSheetName(defaultSheet, sheetName); err != nil {
			return fmt.Errorf("rename xlsx sheet: %w", err)
		}
	}

	rows := make([][]any, 0, len(w.records)+1)
	if len(w.headers) > 0 {
		headerRow := make([]any, len(w.headers))
		for i, header := range w.headers {
			headerRow[i] = header
		}
		rows = append(rows, headerRow)
	}
	for _, rec := range w.records {
		row := make([]any, len(w.headers))
		for i, header := range w.headers {
			row[i] = stringifyCell(rec[header])
		}
		rows = append(rows, row)
	}
	for rowIdx, row := range rows {
		cell, err := excelize.CoordinatesToCellName(1, rowIdx+1)
		if err != nil {
			return fmt.Errorf("resolve xlsx cell: %w", err)
		}
		if err := f.SetSheetRow(sheetName, cell, &row); err != nil {
			return fmt.Errorf("write xlsx row %d: %w", rowIdx+1, err)
		}
	}
	if err := f.SaveAs(w.path); err != nil {
		return fmt.Errorf("save xlsx output: %w", err)
	}
	return nil
}
