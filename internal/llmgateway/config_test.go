package llmgateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "cfg.yaml")
	data := []byte(`
backends:
  - name: local
    url: http://localhost:1234/v1
routes:
  - name: chat
    backends: [local]
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Servers.OpenAIAddr != ":1234" || cfg.Servers.OllamaAddr != ":11434" {
		t.Fatalf("unexpected default addresses: %+v", cfg.Servers)
	}
	if cfg.Backends[0].Weight != 1 {
		t.Fatalf("expected default weight=1, got %d", cfg.Backends[0].Weight)
	}
}

func TestLoadConfigRejectUnknownBackend(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "cfg.yaml")
	data := []byte(`
backends:
  - name: local
    url: http://localhost:1234/v1
routes:
  - name: chat
    backends: [missing]
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error for unknown backend")
	}
}
