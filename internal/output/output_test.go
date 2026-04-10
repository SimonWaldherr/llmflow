package output

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

func TestCSVWriter_Basic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.csv")
	cfg := config.OutputConfig{Type: "csv", Path: p, CSV: config.CSVConfig{Delimiter: ","}}
	w, err := NewCSVWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	records := []Record{
		{"name": "Alice", "city": "Berlin"},
		{"name": "Bob", "city": "Hamburg"},
	}
	if err := w.WriteAll(context.Background(), records); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 { // header + 2 data rows
		t.Fatalf("expected 3 rows (header + 2 data), got %d", len(rows))
	}
}

func TestCSVWriter_Empty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.csv")
	cfg := config.OutputConfig{Type: "csv", Path: p}
	w, err := NewCSVWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteAll(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	w.Close()
}

func TestCSVWriter_CustomDelimiter(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.csv")
	cfg := config.OutputConfig{Type: "csv", Path: p, CSV: config.CSVConfig{Delimiter: ";"}}
	w, err := NewCSVWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	records := []Record{{"a": "1", "b": "2"}}
	if err := w.WriteAll(context.Background(), records); err != nil {
		t.Fatal(err)
	}
	w.Close()

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !containsRune(string(data), ';') {
		t.Error("expected semicolon delimiter in output")
	}
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

func TestJSONLWriter_Basic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.jsonl")
	cfg := config.OutputConfig{Type: "jsonl", Path: p}
	w, err := NewJSONLWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	records := []Record{
		{"name": "Alice"},
		{"name": "Bob"},
	}
	if err := w.WriteAll(context.Background(), records); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var lines []map[string]any
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lines[0]["name"] != "Alice" {
		t.Errorf("unexpected first record: %v", lines[0])
	}
}

func TestJSONLWriter_Empty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.jsonl")
	cfg := config.OutputConfig{Type: "jsonl", Path: p}
	w, err := NewJSONLWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteAll(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	w.Close()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Errorf("expected empty file, got size %d", info.Size())
	}
}
