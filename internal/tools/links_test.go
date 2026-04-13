package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebExtractLinks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`
<html>
  <body>
    <a href="/docs/start">Start</a>
    <a href="https://example.org/api">API</a>
    <a href="mailto:test@example.org">Mail</a>
    <a href="/docs/start">Duplicate</a>
  </body>
</html>
`))
	}))
	defer srv.Close()

	out, err := webExtractLinks(context.Background(), `{"url":"`+srv.URL+`","max_links":10}`)
	if err != nil {
		t.Fatalf("webExtractLinks returned error: %v", err)
	}

	var links []string
	if err := json.Unmarshal([]byte(out), &links); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d (%v)", len(links), links)
	}
	if links[0] != srv.URL+"/docs/start" {
		t.Fatalf("unexpected first link: %q", links[0])
	}
	if links[1] != "https://example.org/api" {
		t.Fatalf("unexpected second link: %q", links[1])
	}
}
