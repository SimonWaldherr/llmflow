package tools

// links.go implements a web utility tool that extracts absolute links from HTML.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var hrefAttrRe = regexp.MustCompile(`(?i)href\s*=\s*['"]([^'"#]+)['"]`)

// NewWebExtractLinksTool returns a Tool that extracts absolute links from a URL.
func NewWebExtractLinksTool() Tool {
	return Tool{
		Name:        "web_extract_links",
		Description: "Fetches a web page and extracts absolute HTTP(S) links. Useful for quickly discovering relevant pages before deeper fetching.",
		Parameters: []byte(`{
  "type": "object",
  "properties": {
    "url": {
      "type": "string",
      "description": "The full URL to fetch (must start with http:// or https://)"
    },
    "max_links": {
      "type": "integer",
      "description": "Maximum number of links to return (default 20, max 100)"
    }
  },
  "required": ["url"]
}`),
		Execute: webExtractLinks,
	}
}

func webExtractLinks(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		URL      string `json:"url"`
		MaxLinks int    `json:"max_links"`
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

	maxLinks := args.MaxLinks
	if maxLinks <= 0 {
		maxLinks = 20
	}
	if maxLinks > 100 {
		maxLinks = 100
	}

	baseURL, err := url.Parse(args.URL)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
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

	matches := hrefAttrRe.FindAllStringSubmatch(string(body), -1)
	seen := make(map[string]struct{}, len(matches))
	links := make([]string, 0, maxLinks)

	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		raw := strings.TrimSpace(m[1])
		if raw == "" {
			continue
		}
		ref, err := url.Parse(raw)
		if err != nil {
			continue
		}
		abs := baseURL.ResolveReference(ref)
		if abs.Scheme != "http" && abs.Scheme != "https" {
			continue
		}
		normalized := abs.String()
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		links = append(links, normalized)
		if len(links) >= maxLinks {
			break
		}
	}

	result, err := json.Marshal(links)
	if err != nil {
		return "", fmt.Errorf("marshal links: %w", err)
	}
	return string(result), nil
}
