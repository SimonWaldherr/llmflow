package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodeExecuteBasic(t *testing.T) {
	tool := NewCodeExecTool(CodeExecConfig{
		Timeout:        2 * time.Second,
		MaxSourceBytes: 16 * 1024,
	})

	args := map[string]any{
		"source": `package main
import "fmt"
func main() {
	fmt.Println("hello from nanogo")
}`,
	}
	b, _ := json.Marshal(args)

	out, err := tool.Execute(context.Background(), string(b))
	if err != nil {
		t.Fatalf("code_execute returned error: %v", err)
	}
	if !strings.Contains(out, "hello from nanogo") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestCodeExecuteRejectsOversizeSource(t *testing.T) {
	tool := NewCodeExecTool(CodeExecConfig{
		Timeout:        2 * time.Second,
		MaxSourceBytes: 32,
	})

	args := map[string]any{
		"source": strings.Repeat("a", 128),
	}
	b, _ := json.Marshal(args)

	_, err := tool.Execute(context.Background(), string(b))
	if err == nil {
		t.Fatal("expected size validation error")
	}
	if !strings.Contains(err.Error(), "max_source_bytes") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveReadablePathWhitelist(t *testing.T) {
	dir := t.TempDir()
	allowedFile := filepath.Join(dir, "allowed", "data.txt")
	if err := os.MkdirAll(filepath.Dir(allowedFile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(allowedFile, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	full, err := resolveReadablePath(dir, "allowed/data.txt", []string{"allowed"})
	if err != nil {
		t.Fatalf("expected allowed path, got error: %v", err)
	}
	if full != allowedFile {
		t.Fatalf("unexpected resolved path: %s", full)
	}

	if _, err := resolveReadablePath(dir, "../etc/passwd", []string{"allowed"}); err == nil {
		t.Fatal("expected parent traversal to be denied")
	}
	if _, err := resolveReadablePath(dir, "denied/file.txt", []string{"allowed"}); err == nil {
		t.Fatal("expected non-whitelisted path to be denied")
	}
}
