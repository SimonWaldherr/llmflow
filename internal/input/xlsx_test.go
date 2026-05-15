package input

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SimonWaldherr/llmflow/internal/config"
	"github.com/xuri/excelize/v2"
)

func TestXLSXReader_WithHeader(t *testing.T) {
	p := filepath.Join(t.TempDir(), "data.xlsx")
	f := excelize.NewFile()
	if err := f.SetSheetRow("Sheet1", "A1", &[]any{"name", "city"}); err != nil {
		t.Fatal(err)
	}
	if err := f.SetSheetRow("Sheet1", "A2", &[]any{"Alice", "Berlin"}); err != nil {
		t.Fatal(err)
	}
	if err := f.SetSheetRow("Sheet1", "A3", &[]any{"Bob", "Hamburg"}); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveAs(p); err != nil {
		t.Fatal(err)
	}

	cfg := config.InputConfig{Type: "xlsx", Path: p, CSV: config.CSVConfig{HasHeader: true}}
	r, err := NewXLSXReader(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	recs, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("record count = %d, want 2", len(recs))
	}
	if recs[0]["name"] != "Alice" || recs[1]["city"] != "Hamburg" {
		t.Fatalf("unexpected records: %#v", recs)
	}
}
