package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

func TestNormalizeHostBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                        "",
		"localhost:11434":         "http://localhost:11434",
		"http://127.0.0.1:11434":  "http://127.0.0.1:11434",
		"https://example.org/foo": "https://example.org",
		"::invalid::":             "",
	}
	for in, want := range cases {
		if got := normalizeHostBaseURL(in); got != want {
			t.Fatalf("normalizeHostBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeLMStudioBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"localhost:1234":      "http://localhost:1234/v1",
		"http://127.0.0.1:80": "http://127.0.0.1:80/v1",
	}
	for in, want := range cases {
		if got := normalizeLMStudioBaseURL(in); got != want {
			t.Fatalf("normalizeLMStudioBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeModelNames(t *testing.T) {
	in := []string{" llama3", "mistral", "llama3", "", "  mistral:7b "}
	got := normalizeModelNames(in)
	want := []string{"llama3", "mistral", "mistral:7b"}
	if len(got) != len(want) {
		t.Fatalf("len(normalizeModelNames) = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeModelNames[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildDetectCandidatesUsesEnvOverrides(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "192.168.1.20:11434")
	t.Setenv("LLMFLOW_LMSTUDIO_BASE_URL", "192.168.1.21:1234")

	candidates := buildDetectCandidates()

	var foundOllama, foundLMStudio bool
	for _, c := range candidates {
		if c.provider == config.ProviderOllama && c.baseURL == "http://192.168.1.20:11434" {
			foundOllama = true
		}
		if c.provider == config.ProviderLMStudio && c.baseURL == "http://192.168.1.21:1234/v1" {
			foundLMStudio = true
		}
	}
	if !foundOllama {
		t.Fatal("expected ollama candidate from OLLAMA_HOST")
	}
	if !foundLMStudio {
		t.Fatal("expected lmstudio candidate from LLMFLOW_LMSTUDIO_BASE_URL")
	}
}

func TestResolveQuickFormAPIKey(t *testing.T) {
	direct, env := resolveQuickFormAPIKey("sk-test-direct")
	if direct != "sk-test-direct" || env != "" {
		t.Fatalf("expected direct key, got direct=%q env=%q", direct, env)
	}

	direct, env = resolveQuickFormAPIKey("OPENAI_API_KEY")
	if direct != "" || env != "OPENAI_API_KEY" {
		t.Fatalf("expected env var name, got direct=%q env=%q", direct, env)
	}

	direct, env = resolveQuickFormAPIKey("   ")
	if direct != "" || env != "" {
		t.Fatalf("expected empty key config, got direct=%q env=%q", direct, env)
	}
}

func TestResolveSuggestTimeout_Default(t *testing.T) {
	t.Setenv("LLMFLOW_WEB_SUGGEST_TIMEOUT", "")

	got, err := resolveSuggestTimeout("")
	if err != nil {
		t.Fatalf("resolveSuggestTimeout returned error: %v", err)
	}
	if got != 120*time.Second {
		t.Fatalf("expected 120s default, got %s", got)
	}
}

func TestResolveSuggestTimeout_UsesRequestOverride(t *testing.T) {
	t.Setenv("LLMFLOW_WEB_SUGGEST_TIMEOUT", "90s")

	got, err := resolveSuggestTimeout("5m")
	if err != nil {
		t.Fatalf("resolveSuggestTimeout returned error: %v", err)
	}
	if got != 5*time.Minute {
		t.Fatalf("expected 5m from request override, got %s", got)
	}
}

func TestResolveSuggestTimeout_ClampsAndValidates(t *testing.T) {
	t.Setenv("LLMFLOW_WEB_SUGGEST_TIMEOUT", "1s")

	got, err := resolveSuggestTimeout("")
	if err != nil {
		t.Fatalf("resolveSuggestTimeout returned error: %v", err)
	}
	if got != 10*time.Second {
		t.Fatalf("expected lower clamp to 10s, got %s", got)
	}

	got, err = resolveSuggestTimeout("20m")
	if err != nil {
		t.Fatalf("resolveSuggestTimeout returned error: %v", err)
	}
	if got != 10*time.Minute {
		t.Fatalf("expected upper clamp to 10m, got %s", got)
	}

	if _, err := resolveSuggestTimeout("invalid"); err == nil {
		t.Fatal("expected invalid request timeout to fail")
	}

	t.Setenv("LLMFLOW_WEB_SUGGEST_TIMEOUT", "nope")
	if _, err := resolveSuggestTimeout(""); err == nil {
		t.Fatal("expected invalid env timeout to fail")
	}
}

func TestParseConfigAcceptsJSON(t *testing.T) {
	jsonConfig := `{
		"api": {"model": "gpt-test", "api_key": "sk-test-direct"},
		"prompt": {"input_template": "{{ .record }}"},
		"input": {"type": "csv"},
		"output": {"type": "jsonl"}
	}`

	cfg, err := parseConfig(jsonConfig)
	if err != nil {
		t.Fatalf("parseConfig rejected json: %v", err)
	}
	if cfg.API.Model != "gpt-test" {
		t.Fatalf("model = %q, want gpt-test", cfg.API.Model)
	}
	if cfg.API.APIKeyDirect != "sk-test-direct" {
		t.Fatalf("api key direct = %q, want direct key", cfg.API.APIKeyDirect)
	}
}

func TestHandleModelsOpenAICompatible(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test-direct" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "gpt-4o"},
				{"id": "gpt-4o-mini"},
			},
		})
	}))
	t.Cleanup(providerServer.Close)

	reqBody := map[string]string{
		"provider": config.ProviderOpenAI,
		"base_url": providerServer.URL,
		"api_key":  "sk-test-direct",
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/models", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	(&Server{}).handleModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("expected ok response, got %#v", resp)
	}
	models, ok := resp.Data.([]any)
	if !ok {
		t.Fatalf("expected models array, got %#v", resp.Data)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %#v", models)
	}
}
