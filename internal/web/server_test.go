package web

import (
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
