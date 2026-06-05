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

func TestLoadConfigDefaultHealthByBackendType(t *testing.T) {
	t.Parallel()
	cfg, err := LoadConfigBytes([]byte(`
backends:
  - name: azure
    type: azure
    url: https://example.com
  - name: gemini
    type: gemini
    url: https://example.com/v1beta
  - name: anthropic
    type: anthropic
    url: https://example.com
routes:
  - name: default
    backends: [azure]
`))
	if err != nil {
		t.Fatalf("LoadConfigBytes: %v", err)
	}

	healthByName := map[string]string{}
	for _, b := range cfg.Backends {
		healthByName[b.Name] = b.HealthPath
	}
	if got := healthByName["azure"]; got != "/v1/models" {
		t.Fatalf("azure health path = %q, want /v1/models", got)
	}
	if got := healthByName["gemini"]; got != "/models" {
		t.Fatalf("gemini health path = %q, want /models", got)
	}
	if got := healthByName["anthropic"]; got != "/v1/models" {
		t.Fatalf("anthropic health path = %q, want /v1/models", got)
	}
}
