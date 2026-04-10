package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(config.APIConfig{
		BaseURL:         srv.URL,
		Model:           "test-model",
		Timeout:         5 * time.Second,
		MaxOutputTokens: 100,
	}, "test-api-key")
}

func TestGenerateSuccess(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-key" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		_ = json.NewEncoder(w).Encode(responsesResponse{OutputText: "Hello from LLM"})
	})

	text, err := client.Generate(context.Background(), "system", "user input")
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hello from LLM" {
		t.Fatalf("unexpected output: %q", text)
	}
}

func TestGenerateAPIError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(responsesResponse{
			Error: &struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			}{Message: "quota exceeded", Type: "quota_error"},
		})
	})

	if _, err := client.Generate(context.Background(), "", "input"); err == nil {
		t.Fatal("expected error from API error response")
	}
}

func TestGenerateHTTPError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	})

	if _, err := client.Generate(context.Background(), "", "input"); err == nil {
		t.Fatal("expected error for non-2xx status")
	}
}

func TestGenerateInvalidJSON(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	})

	if _, err := client.Generate(context.Background(), "", "input"); err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestBackoff(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 1 * time.Second},
		{attempt: 2, want: 2 * time.Second},
		{attempt: 3, want: 4 * time.Second},
		{attempt: 0, want: 1 * time.Second},
		{attempt: 10, want: 32 * time.Second},
	}
	for _, tc := range cases {
		if got := Backoff(tc.attempt); got != tc.want {
			t.Fatalf("Backoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}
