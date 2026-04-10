package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const validYAML = `
api:
  base_url: https://api.example.com/v1
  api_key_env: TEST_KEY
  model: gpt-test
  timeout: 30s
  max_output_tokens: 500
prompt:
  system: "You are a helper."
  input_template: "{{ .record }}"
input:
  type: csv
  path: ./testdata/input.csv
  csv:
    delimiter: ","
    has_header: true
output:
  type: jsonl
  path: ./testdata/output.jsonl
processing:
  mode: per_record
  response_field: result
`

func TestLoadYAML(t *testing.T) {
	p := writeTempFile(t, "cfg.yaml", validYAML)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.API.Model != "gpt-test" {
		t.Fatalf("model = %q, want gpt-test", cfg.API.Model)
	}
	if cfg.API.Timeout != 30*time.Second {
		t.Fatalf("timeout = %v, want 30s", cfg.API.Timeout)
	}
}

func TestLoadJSON(t *testing.T) {
	content := `{
		"api": {"base_url": "https://api.example.com/v1", "api_key_env": "K", "model": "m"},
		"prompt": {"input_template": "{{ .record }}"},
		"input": {"type": "csv"},
		"output": {"type": "jsonl"}
	}`
	p := writeTempFile(t, "cfg.json", content)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.API.Model != "m" {
		t.Fatalf("model = %q, want m", cfg.API.Model)
	}
}

func TestLoadUnsupportedExtension(t *testing.T) {
	p := writeTempFile(t, "cfg.toml", "")
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for unsupported extension")
	}
}

func TestLoadFileNotFound(t *testing.T) {
	if _, err := Load("/nonexistent/path/config.yaml"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestValidateMissingFields(t *testing.T) {
	if err := (Config{}).Validate(); err == nil {
		t.Fatal("expected validation error for empty config")
	}
}

func TestValidateUnsupportedInputType(t *testing.T) {
	c := Config{
		API:    APIConfig{Model: "m", APIKeyEnv: "K"},
		Input:  InputConfig{Type: "parquet"},
		Output: OutputConfig{Type: "jsonl"},
		Prompt: PromptConfig{InputTemplate: "x"},
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for unsupported input type")
	}
}

func TestApplyDefaults(t *testing.T) {
	c := Config{}
	c.ApplyDefaults()
	if c.API.Provider != ProviderOpenAI {
		t.Fatalf("provider = %q, want %q", c.API.Provider, ProviderOpenAI)
	}
	if c.API.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("base_url = %q, want default openai url", c.API.BaseURL)
	}
	if c.API.Timeout != 60*time.Second {
		t.Fatal("expected default timeout")
	}
	if c.Processing.Mode != "per_record" {
		t.Fatal("expected default mode")
	}
	if c.Processing.ResponseField != "response" {
		t.Fatal("expected default response field")
	}
	if c.Input.CSV.Delimiter != "," {
		t.Fatal("expected default input delimiter")
	}
	if c.Output.CSV.Delimiter != "," {
		t.Fatal("expected default output delimiter")
	}
}

func TestAPIKeyPresent(t *testing.T) {
	t.Setenv("TEST_LLMFLOW_KEY", "secret")
	c := Config{API: APIConfig{APIKeyEnv: "TEST_LLMFLOW_KEY"}}
	k, err := c.APIKey()
	if err != nil {
		t.Fatal(err)
	}
	if k != "secret" {
		t.Fatalf("got %q, want secret", k)
	}
}

func TestAPIKeyMissing(t *testing.T) {
	c := Config{API: APIConfig{APIKeyEnv: "LLMFLOW_NONEXISTENT_KEY_12345"}}
	if _, err := c.APIKey(); err == nil {
		t.Fatal("expected error for missing env var")
	}
}

func TestAPIKeyNoKeyProviders(t *testing.T) {
	providers := []string{ProviderOllama, ProviderLMStudio}
	for _, provider := range providers {
		c := Config{API: APIConfig{Provider: provider, Model: "model"}}
		k, err := c.APIKey()
		if err != nil {
			t.Fatalf("provider %s should not require key: %v", provider, err)
		}
		if k != "" {
			t.Fatalf("provider %s returned non-empty key %q", provider, k)
		}
	}
}

func TestResolveSecret(t *testing.T) {
	t.Setenv("MY_SECRET", "val")
	if got := ResolveSecret("direct", "MY_SECRET"); got != "direct" {
		t.Fatalf("expected direct, got %q", got)
	}
	if got := ResolveSecret("", "MY_SECRET"); got != "val" {
		t.Fatalf("expected env value, got %q", got)
	}
	if got := ResolveSecret("", ""); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestProviderDefaultBaseURL(t *testing.T) {
	cases := map[string]string{
		ProviderOpenAI:    "https://api.openai.com/v1",
		ProviderOllama:    "http://localhost:11434",
		ProviderLMStudio:  "http://localhost:1234/v1",
		ProviderGemini:    "https://generativelanguage.googleapis.com/v1beta",
		ProviderAnthropic: "https://api.anthropic.com/v1",
		ProviderGeneric:   "https://api.openai.com/v1",
	}
	for provider, want := range cases {
		if got := providerDefaultBaseURL(provider); got != want {
			t.Fatalf("provider %s default url = %q, want %q", provider, got, want)
		}
	}
}
