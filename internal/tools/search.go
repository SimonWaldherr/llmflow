package tools

// search.go implements a web-search tool backed by the DuckDuckGo Instant
// Answer API (free, no API key required).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// NewWebSearchTool returns a Tool that searches the web via DuckDuckGo.
func NewWebSearchTool() Tool {
	return Tool{
		Name:        "web_search",
		Description: "Searches the web using DuckDuckGo and returns a summary of the top results. Use this to look up current events, facts, or any information you don't already know.",
		Parameters: []byte(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "The search query"
    }
  },
  "required": ["query"]
}`),
		Execute: webSearch,
	}
}

func webSearch(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Query == "" {
		return "", fmt.Errorf("query is required")
	}

	endpoint := "https://api.duckduckgo.com/?" + url.Values{
		"q":             {args.Query},
		"format":        {"json"},
		"no_html":       {"1"},
		"skip_disambig": {"1"},
	}.Encode()

	body, err := fetchURL(ctx, endpoint, 10*time.Second, 256*1024)
	if err != nil {
		return "", err
	}

	var ddg struct {
		AbstractText  string `json:"AbstractText"`
		AbstractURL   string `json:"AbstractURL"`
		Answer        string `json:"Answer"`
		RelatedTopics []struct {
			Text     string `json:"Text"`
			FirstURL string `json:"FirstURL"`
		} `json:"RelatedTopics"`
	}
	if err := json.Unmarshal(body, &ddg); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	var sb strings.Builder
	if ddg.Answer != "" {
		sb.WriteString("Answer: ")
		sb.WriteString(ddg.Answer)
		sb.WriteString("\n\n")
	}
	if ddg.AbstractText != "" {
		sb.WriteString("Summary: ")
		sb.WriteString(ddg.AbstractText)
		sb.WriteByte('\n')
		if ddg.AbstractURL != "" {
			sb.WriteString("Source: ")
			sb.WriteString(ddg.AbstractURL)
			sb.WriteByte('\n')
		}
		sb.WriteByte('\n')
	}
	for i, t := range ddg.RelatedTopics {
		if i >= 5 {
			break
		}
		if t.Text != "" {
			sb.WriteString("- ")
			sb.WriteString(t.Text)
			sb.WriteByte('\n')
			if t.FirstURL != "" {
				sb.WriteString("  ")
				sb.WriteString(t.FirstURL)
				sb.WriteByte('\n')
			}
		}
	}

	result := strings.TrimSpace(sb.String())
	if result == "" {
		result = "No results found for: " + args.Query
	}
	return result, nil
}
