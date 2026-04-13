package app

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SimonWaldherr/llmflow/internal/config"
	"github.com/SimonWaldherr/llmflow/internal/prompt"
)

// fakeGenerator implements Generator for testing.
type fakeGenerator struct {
	response string
	err      error
	calls    int
}

func (f *fakeGenerator) Generate(_ context.Context, _, _ string) (string, error) {
	f.calls++
	return f.response, f.err
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func baseConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "in.csv")
	outPath := filepath.Join(dir, "out.jsonl")
	os.WriteFile(csvPath, []byte("name\nAlice\n"), 0o600)

	return config.Config{
		API: config.APIConfig{
			BaseURL:   "https://api.example.com/v1",
			APIKeyEnv: "LLMFLOW_TEST_API_KEY",
			Model:     "test-model",
		},
		Prompt: config.PromptConfig{
			InputTemplate: "Name: {{ .name }}",
		},
		Input: config.InputConfig{
			Type: "csv",
			Path: csvPath,
			CSV:  config.CSVConfig{Delimiter: ",", HasHeader: true},
		},
		Output: config.OutputConfig{
			Type: "jsonl",
			Path: outPath,
		},
		Processing: config.ProcessingConfig{
			ResponseField:        "result",
			IncludeInputInOutput: true,
			Workers:              1,
		},
	}
}

func newTestPromptBuilder(t *testing.T) *prompt.Builder {
	t.Helper()
	pb, err := prompt.New(config.PromptConfig{InputTemplate: "Hello {{ .name }}"})
	if err != nil {
		t.Fatal(err)
	}
	return pb
}

func TestApp_DryRun(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Processing.DryRun = true

	a := New(cfg, newTestLogger())
	if err := a.Run(context.Background()); err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}
}

func TestApp_WithDryRun_Override(t *testing.T) {
	cfg := baseConfig(t)
	a := New(cfg, newTestLogger()).WithDryRun(true)
	if err := a.Run(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProcessRecords_SingleWorker(t *testing.T) {
	cfg := baseConfig(t)
	a := New(cfg, newTestLogger())
	gen := &fakeGenerator{response: "ok"}
	pb := newTestPromptBuilder(t)

	records := []map[string]any{{"name": "Alice"}, {"name": "Bob"}}
	results, err := a.processRecords(context.Background(), gen, pb, nil, records, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2, got %d", len(results))
	}
	if gen.calls != 2 {
		t.Errorf("expected 2 generate calls, got %d", gen.calls)
	}
}

func TestProcessRecords_MultiWorker(t *testing.T) {
	cfg := baseConfig(t)
	a := New(cfg, newTestLogger())
	gen := &fakeGenerator{response: "ok"}
	pb := newTestPromptBuilder(t)

	records := make([]map[string]any, 10)
	for i := range records {
		records[i] = map[string]any{"name": "item"}
	}
	results, err := a.processRecords(context.Background(), gen, pb, nil, records, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 10 {
		t.Fatalf("expected 10, got %d", len(results))
	}
}

func TestProcessRecords_ContinueOnError(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Processing.ContinueOnError = true
	a := New(cfg, newTestLogger())

	gen := &fakeGenerator{err: context.DeadlineExceeded}
	pb := newTestPromptBuilder(t)

	records := []map[string]any{{"name": "A"}, {"name": "B"}}
	results, err := a.processRecords(context.Background(), gen, pb, nil, records, 1, nil)
	if err != nil {
		t.Fatalf("unexpected error with continue_on_error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestProcessRecords_StopOnError(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Processing.ContinueOnError = false
	a := New(cfg, newTestLogger())

	gen := &fakeGenerator{err: context.DeadlineExceeded}
	pb := newTestPromptBuilder(t)

	records := []map[string]any{{"name": "A"}}
	_, err := a.processRecords(context.Background(), gen, pb, nil, records, 1, nil)
	if err == nil {
		t.Fatal("expected error when continue_on_error is false")
	}
}

func TestProcessRecords_ResultCallback(t *testing.T) {
	cfg := baseConfig(t)
	a := New(cfg, newTestLogger())
	gen := &fakeGenerator{response: "ok"}
	pb := newTestPromptBuilder(t)

	var got int
	a.WithResultFunc(func(_ int, _ int, record map[string]any) {
		if record["result"] == "ok" {
			got++
		}
	})

	records := []map[string]any{{"name": "Alice"}, {"name": "Bob"}}
	_, err := a.processRecords(context.Background(), gen, pb, nil, records, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("expected result callback to fire twice, got %d", got)
	}
}

func TestBuildTools_SQLiteUsesInputPathFallback(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "lookup.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE items (name TEXT); INSERT INTO items(name) VALUES ('alpha'), ('beta');`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}

	cfg := baseConfig(t)
	cfg.Input.Type = "sqlite"
	cfg.Input.Path = dbPath
	cfg.Tools.Enabled = true
	cfg.Tools.SQLQuery = true
	cfg.Tools.SQL.Driver = "sqlite"
	cfg.Tools.SQL.DSN = ""
	cfg.Tools.SQL.DSNEnv = ""

	a := New(cfg, newTestLogger())
	ts := a.buildTools()
	if len(ts) != 1 {
		t.Fatalf("expected 1 enabled tool, got %d", len(ts))
	}
	if ts[0].Name != "sql_query" {
		t.Fatalf("expected sql_query tool, got %q", ts[0].Name)
	}

	out, err := ts[0].Execute(context.Background(), `{"query":"SELECT name FROM items ORDER BY name"}`)
	if err != nil {
		t.Fatalf("sql_query execute failed: %v", err)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Fatalf("unexpected sql_query output: %s", out)
	}
}

func TestBuildTools_SQLitePrefersExplicitDSN(t *testing.T) {
	dir := t.TempDir()
	explicitPath := filepath.Join(dir, "explicit.db")
	fallbackPath := filepath.Join(dir, "fallback.db")

	seedDB := func(path, value string) {
		t.Helper()
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatalf("open sqlite %s: %v", path, err)
		}
		defer db.Close()
		if _, err := db.Exec(`CREATE TABLE items (name TEXT); INSERT INTO items(name) VALUES (?);`, value); err != nil {
			t.Fatalf("seed sqlite %s: %v", path, err)
		}
	}

	seedDB(explicitPath, "explicit")
	seedDB(fallbackPath, "fallback")

	cfg := baseConfig(t)
	cfg.Input.Type = "sqlite"
	cfg.Input.Path = fallbackPath
	cfg.Tools.Enabled = true
	cfg.Tools.SQLQuery = true
	cfg.Tools.SQL.Driver = "sqlite"
	cfg.Tools.SQL.DSN = explicitPath

	a := New(cfg, newTestLogger())
	ts := a.buildTools()
	if len(ts) != 1 {
		t.Fatalf("expected 1 enabled tool, got %d", len(ts))
	}

	out, err := ts[0].Execute(context.Background(), `{"query":"SELECT name FROM items"}`)
	if err != nil {
		t.Fatalf("sql_query execute failed: %v", err)
	}
	if !strings.Contains(out, "explicit") {
		t.Fatalf("expected query against explicit DSN, output: %s", out)
	}
	if strings.Contains(out, "fallback") {
		t.Fatalf("unexpected fallback record in output: %s", out)
	}
}

func TestBuildTools_CodeExecuteEnabled(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Tools.Enabled = true
	cfg.Tools.CodeExecute = true
	cfg.Tools.Code.Timeout = 2 * time.Second

	a := New(cfg, newTestLogger())
	ts := a.buildTools()
	if len(ts) != 1 {
		t.Fatalf("expected 1 enabled tool, got %d", len(ts))
	}
	if ts[0].Name != "code_execute" {
		t.Fatalf("expected code_execute tool, got %q", ts[0].Name)
	}
}
