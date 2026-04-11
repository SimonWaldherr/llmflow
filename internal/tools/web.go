package tools

// web.go implements a web-fetch tool that retrieves the text content of a URL.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var (
	htmlTagRe  = regexp.MustCompile(`<[^>]+>`)
	whitespace = regexp.MustCompile(`\s+`)
)

// NewWebFetchTool returns a Tool that fetches the text content of a URL.
func NewWebFetchTool() Tool {
	return Tool{
		Name:        "web_fetch",
		Description: "Fetches the text content of a web page at the given URL. Useful for reading documentation, articles, or any publicly accessible page.",
		Parameters: []byte(`{
  "type": "object",
  "properties": {
    "url": {
      "type": "string",
      "description": "The full URL to fetch (must start with http:// or https://)"
    }
  },
  "required": ["url"]
}`),
		Execute: webFetch,
	}
}

func webFetch(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.URL == "" {
		return "", fmt.Errorf("url is required")
	}
	if !strings.HasPrefix(args.URL, "http://") && !strings.HasPrefix(args.URL, "https://") {
		return "", fmt.Errorf("url must start with http:// or https://")
	}

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, args.URL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "llmflow-agent/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	// Strip HTML tags and normalize whitespace.
	text := htmlTagRe.ReplaceAllString(string(body), " ")
	text = whitespace.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)

	const maxLen = 6000
	if len(text) > maxLen {
		text = text[:maxLen] + "... [truncated]"
	}
	return text, nil
}
