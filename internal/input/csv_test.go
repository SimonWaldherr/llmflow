package input

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

func writeTempCSV(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCSVReaderWithHeader(t *testing.T) {
	p := writeTempCSV(t, "data.csv", "name,age\nAlice,30\nBob,25\n")
	r, err := NewCSVReader(config.InputConfig{
		Type: "csv",
		Path: p,
		CSV:  config.CSVConfig{Delimiter: ",", HasHeader: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	recs, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0]["name"] != "Alice" || recs[1]["age"] != "25" {
		t.Fatalf("unexpected records: %#v", recs)
	}
}

func TestCSVReaderNextWithHeader(t *testing.T) {
	p := writeTempCSV(t, "data.csv", "name,age\nAlice,30\nBob,25\n")
	r, err := NewCSVReader(config.InputConfig{
		Type: "csv",
		Path: p,
		CSV:  config.CSVConfig{Delimiter: ",", HasHeader: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	first, err := r.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first["name"] != "Alice" || second["age"] != "25" {
		t.Fatalf("unexpected streamed records: %#v %#v", first, second)
	}
}

func TestCSVReaderWithoutHeader(t *testing.T) {
	p := writeTempCSV(t, "data.csv", "Alice,30\nBob,25\n")
	r, err := NewCSVReader(config.InputConfig{
		Type: "csv",
		Path: p,
		CSV:  config.CSVConfig{Delimiter: ",", HasHeader: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	recs, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0]["col_1"] != "Alice" || recs[0]["col_2"] != "30" {
		t.Fatalf("unexpected records: %#v", recs)
	}
}

func TestCSVReaderCustomDelimiter(t *testing.T) {
	p := writeTempCSV(t, "data.csv", "name;city\nAlice;Berlin\n")
	r, err := NewCSVReader(config.InputConfig{
		Type: "csv",
		Path: p,
		CSV:  config.CSVConfig{Delimiter: ";", HasHeader: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	recs, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0]["city"] != "Berlin" {
		t.Fatalf("unexpected records: %#v", recs)
	}
}

func TestCSVReaderAutoDetectDelimiter(t *testing.T) {
	p := writeTempCSV(t, "data.csv", "name;city\nAlice;Berlin\n")
	r, err := NewCSVReader(config.InputConfig{
		Type: "csv",
		Path: p,
		CSV:  config.CSVConfig{HasHeader: true}, // Delimiter intentionally empty.
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	recs, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0]["city"] != "Berlin" {
		t.Fatalf("unexpected records: %#v", recs)
	}
}

func TestCSVReaderVariableFieldCounts(t *testing.T) {
	p := writeTempCSV(t, "data.csv", "name,age,city\nAlice,30\nBob,25,Berlin,extra\n")
	r, err := NewCSVReader(config.InputConfig{
		Type: "csv",
		Path: p,
		CSV:  config.CSVConfig{Delimiter: ",", HasHeader: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	recs, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error for variable field counts: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0]["city"] != "" {
		t.Fatalf("expected missing city to be empty, got %#v", recs[0]["city"])
	}
	if recs[1]["city"] != "Berlin" {
		t.Fatalf("expected city Berlin, got %#v", recs[1]["city"])
	}
}

func TestCSVReaderEmpty(t *testing.T) {
	p := writeTempCSV(t, "data.csv", "")
	r, err := NewCSVReader(config.InputConfig{
		Type: "csv",
		Path: p,
		CSV:  config.CSVConfig{HasHeader: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	recs, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("expected 0 records, got %d", len(recs))
	}
}

func TestCSVReaderFileNotFound(t *testing.T) {
	if _, err := NewCSVReader(config.InputConfig{Type: "csv", Path: "/nonexistent/file.csv"}); err == nil {
		t.Fatal("expected error for missing file")
	}
}
