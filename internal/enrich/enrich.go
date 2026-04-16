// Package enrich provides a non-agentic web-crawl enrichment step.
// For each input record it takes the value of a configured column, runs a
// DuckDuckGo search, fetches the first result URL, and stores the plain-text
// content in a new output field on the record.
package enrich

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

	"github.com/SimonWaldherr/llmflow/internal/config"
)

var (
	htmlTagRe  = regexp.MustCompile(`<[^>]+>`)
	whitespace = regexp.MustCompile(`\s+`)
)

const defaultMaxChars = 2000

// Enricher performs web-crawl enrichment for records.
type Enricher struct {
	cfg    config.EnrichConfig
	client *http.Client
}

// New returns an Enricher for the given config.
func New(cfg config.EnrichConfig) *Enricher {
	return &Enricher{
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Enrich adds the enrichment field to rec in-place and returns it.
// If the source column is missing or the lookup fails the record is returned
// unchanged (error is non-nil but the caller may choose to continue).
func (e *Enricher) Enrich(ctx context.Context, rec map[string]any) (map[string]any, error) {
	if !e.cfg.Enabled || e.cfg.Column == "" {
		return rec, nil
	}

	raw, ok := rec[e.cfg.Column]
	if !ok {
		return rec, nil
	}
	query := strings.TrimSpace(fmt.Sprint(raw))
	if query == "" {
		return rec, nil
	}

	resultText, err := e.fetchFirstResult(ctx, query)
	if err != nil {
		return rec, fmt.Errorf("enrich %q: %w", query, err)
	}

	maxChars := e.cfg.MaxChars
	if maxChars <= 0 {
		maxChars = defaultMaxChars
	}
	if len(resultText) > maxChars {
		resultText = resultText[:maxChars] + "…"
	}

	outField := e.cfg.OutputField
	if outField == "" {
		outField = "enriched_" + e.cfg.Column
	}
	rec[outField] = resultText
	return rec, nil
}

// fetchFirstResult searches DuckDuckGo for query and fetches the first result URL.
func (e *Enricher) fetchFirstResult(ctx context.Context, query string) (string, error) {
	// 1. DuckDuckGo Instant Answer API (no key required).
	ddgURL := "https://api.duckduckgo.com/?" + url.Values{
		"q":             {query},
		"format":        {"json"},
		"no_html":       {"1"},
		"skip_disambig": {"1"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ddgURL, nil)
	if err != nil {
		return "", fmt.Errorf("create ddg request: %w", err)
	}
	req.Header.Set("User-Agent", "llmflow-enrich/1.0")

	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ddg request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return "", fmt.Errorf("read ddg response: %w", err)
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
		return "", fmt.Errorf("parse ddg response: %w", err)
	}

	// Use the abstract text directly if available (no extra HTTP request needed).
	if ddg.AbstractText != "" {
		text := ddg.AbstractText
		if ddg.Answer != "" {
			text = ddg.Answer + "\n\n" + text
		}
		return text, nil
	}
	if ddg.Answer != "" {
		return ddg.Answer, nil
	}

	// Fall back to fetching the first related-topic URL.
	var firstURL string
	for _, rt := range ddg.RelatedTopics {
		if rt.FirstURL != "" && strings.HasPrefix(rt.FirstURL, "http") {
			firstURL = rt.FirstURL
			break
		}
	}
	if firstURL == "" {
		return "", nil // no result found; caller decides what to do
	}

	return e.fetchPageText(ctx, firstURL)
}

// fetchPageText fetches a URL and returns plain text (HTML stripped).
func (e *Enricher) fetchPageText(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("create page request: %w", err)
	}
	req.Header.Set("User-Agent", "llmflow-enrich/1.0")

	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("page request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return "", fmt.Errorf("read page body: %w", err)
	}

	text := htmlTagRe.ReplaceAllString(string(raw), " ")
	text = whitespace.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)
	return text, nil
}
