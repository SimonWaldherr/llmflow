package input

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

func writeTempJSON(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestJSONReader_Array(t *testing.T) {
	p := writeTempJSON(t, "data.json", `[{"name":"Alice"},{"name":"Bob"}]`)
	cfg := config.InputConfig{Type: "json", Path: p}
	r, err := NewJSONReader(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	recs, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2, got %d", len(recs))
	}
	if recs[0]["name"] != "Alice" {
		t.Errorf("expected Alice, got %v", recs[0]["name"])
	}
}

func TestJSONReader_ArrayNext(t *testing.T) {
	p := writeTempJSON(t, "data.json", `[{"name":"Alice"},{"name":"Bob"}]`)
	cfg := config.InputConfig{Type: "json", Path: p}
	r, err := NewJSONReader(cfg)
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
	if first["name"] != "Alice" || second["name"] != "Bob" {
		t.Fatalf("unexpected streamed records: %#v %#v", first, second)
	}
}

func TestJSONReader_Object(t *testing.T) {
	p := writeTempJSON(t, "data.json", `{"id":"1","val":"x"}`)
	cfg := config.InputConfig{Type: "json", Path: p}
	r, err := NewJSONReader(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	recs, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0]["val"] != "x" {
		t.Errorf("unexpected records: %v", recs)
	}
}

func TestJSONReader_RootPathArray(t *testing.T) {
	p := writeTempJSON(t, "data.json", `{"data":{"items":[{"id":"1"},{"id":"2"}]}}`)
	cfg := config.InputConfig{
		Type: "json",
		Path: p,
		JSON: config.JSONConfig{RootPath: "data.items"},
	}
	r, err := NewJSONReader(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	recs, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[1]["id"] != "2" {
		t.Fatalf("unexpected root_path records: %#v", recs)
	}
}

func TestJSONReader_JSONL(t *testing.T) {
	content := `{"a":"1"}` + "\n" + `{"a":"2"}` + "\n"
	p := writeTempJSON(t, "data.jsonl", content)
	cfg := config.InputConfig{Type: "jsonl", Path: p}
	r, err := NewJSONReader(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	recs, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2, got %d", len(recs))
	}
}

func TestJSONReader_JSONL_Flag(t *testing.T) {
	content := `{"x":"1"}` + "\n" + `{"x":"2"}` + "\n"
	p := writeTempJSON(t, "data.json", content)
	cfg := config.InputConfig{Type: "json", Path: p, JSON: config.JSONConfig{JSONL: true}}
	r, err := NewJSONReader(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	recs, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2, got %d", len(recs))
	}
}

func TestJSONReader_InvalidJSON(t *testing.T) {
	p := writeTempJSON(t, "bad.json", `not-json`)
	cfg := config.InputConfig{Type: "json", Path: p}
	r, err := NewJSONReader(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, err = r.ReadAll(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
