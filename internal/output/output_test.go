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
	"github.com/xuri/excelize/v2"
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

func TestCSVWriter_WriteRecordSyncs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.csv")
	cfg := config.OutputConfig{Type: "csv", Path: p}
	w, err := NewCSVWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Prepare(context.Background(), []string{"name", "city"}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteRecord(context.Background(), Record{"name": "Alice", "city": "Berlin"}); err != nil {
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
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 row, got %d", len(rows))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCSVWriter_AppendsColumnsDiscoveredDuringStreaming(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.csv")
	cfg := config.OutputConfig{Type: "csv", Path: p}
	w, err := NewCSVWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Prepare(context.Background(), []string{"id"}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteRecord(context.Background(), Record{"id": "1"}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteRecord(context.Background(), Record{"id": "2", "answer": "yes"}); err != nil {
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
	want := [][]string{{"id", "answer"}, {"1", ""}, {"2", "yes"}}
	if got, encWant := mustJSON(t, rows), mustJSON(t, want); got != encWant {
		t.Fatalf("rows = %s, want %s", got, encWant)
	}
}

func TestCSVWriter_SerializesStructuredValuesAsJSONCells(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.csv")
	cfg := config.OutputConfig{Type: "csv", Path: p}
	w, err := NewCSVWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	records := []Record{{"id": "1", "meta": map[string]any{"ok": true}}}
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
	if rows[1][1] != `{"ok":true}` {
		t.Fatalf("structured cell = %q, want JSON", rows[1][1])
	}
}

func TestXLSXWriter_BasicPackage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.xlsx")
	cfg := config.OutputConfig{Type: "xlsx", Path: p}
	w, err := NewXLSXWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	records := []Record{
		{"id": "1", "answer": "yes"},
		{"id": "2", "answer": "no"},
	}
	if err := w.WriteAll(context.Background(), records); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := excelize.OpenFile(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if sheets := f.GetSheetList(); len(sheets) != 1 || sheets[0] != "Output" {
		t.Fatalf("sheet list = %#v, want [Output]", sheets)
	}
	rows, err := f.GetRows("Output")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("row count = %d, want 3", len(rows))
	}
	if got, want := rows[0], []string{"answer", "id"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("header row = %#v, want %#v", got, want)
	}
	if got, want := rows[1], []string{"yes", "1"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("first data row = %#v, want %#v", got, want)
	}
	if got, want := rows[2], []string{"no", "2"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("second data row = %#v, want %#v", got, want)
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

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
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

func TestJSONLWriter_WriteRecordSyncs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.jsonl")
	cfg := config.OutputConfig{Type: "jsonl", Path: p}
	w, err := NewJSONLWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteRecord(context.Background(), Record{"name": "Alice"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatal("expected one JSONL line")
	}
	var got map[string]any
	if err := json.Unmarshal(sc.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["name"] != "Alice" {
		t.Fatalf("unexpected record: %#v", got)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
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
