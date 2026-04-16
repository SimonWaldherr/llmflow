package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var jsonPathTokenRe = regexp.MustCompile(`[^\.\[\]]+|\[(\d+)\]`)

// NewTextStatsTool returns a deterministic tool that summarizes basic text metrics.
func NewTextStatsTool() Tool {
	return Tool{
		Name:        "text_stats",
		Description: "Calculates deterministic statistics for a text block such as character, word, line, and paragraph counts. Useful for quick content inspection.",
		Parameters: []byte(`{
  "type": "object",
  "properties": {
    "text": {
      "type": "string",
      "description": "The input text to analyze"
    }
  },
  "required": ["text"]
}`),
		Execute: textStats,
	}
}

// NewRegexExtractTool returns a deterministic tool that extracts regex matches.
func NewRegexExtractTool() Tool {
	return Tool{
		Name:        "regex_extract",
		Description: "Extracts matching substrings from text using a regular expression. Useful for parsing IDs, numbers, codes, and other repeated patterns.",
		Parameters: []byte(`{
  "type": "object",
  "properties": {
    "text": {
      "type": "string",
      "description": "The source text"
    },
    "pattern": {
      "type": "string",
      "description": "The regular expression pattern"
    },
    "group": {
      "type": "integer",
      "description": "Optional capture group to return (0 = full match)"
    },
    "max_matches": {
      "type": "integer",
      "description": "Maximum number of matches to return (default 20, max 100)"
    },
    "case_insensitive": {
      "type": "boolean",
      "description": "Match case-insensitively"
    }
  },
  "required": ["text", "pattern"]
}`),
		Execute: regexExtract,
	}
}

// NewJSONExtractTool returns a deterministic tool that traverses JSON by path.
func NewJSONExtractTool() Tool {
	return Tool{
		Name:        "json_extract",
		Description: "Reads a nested value from a JSON document using a simple dot-and-index path. Useful for deterministic record inspection and normalization.",
		Parameters: []byte(`{
  "type": "object",
  "properties": {
    "json": {
      "type": "string",
      "description": "The JSON document as text"
    },
    "path": {
      "type": "string",
      "description": "A simple path like customer.address.city or items[0].name"
    }
  },
  "required": ["json", "path"]
}`),
		Execute: jsonExtract,
	}
}

func textStats(_ context.Context, argsJSON string) (string, error) {
	var args struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Text) == "" {
		return "", fmt.Errorf("text is required")
	}

	text := args.Text
	stats := map[string]any{
		"characters":  utf8.RuneCountInString(text),
		"bytes":       len(text),
		"words":       len(strings.Fields(text)),
		"lines":       lineCount(text),
		"paragraphs":  paragraphCount(text),
		"trimmed":     strings.TrimSpace(text),
	}
	b, err := json.Marshal(stats)
	if err != nil {
		return "", fmt.Errorf("marshal stats: %w", err)
	}
	return string(b), nil
}

func regexExtract(_ context.Context, argsJSON string) (string, error) {
	var args struct {
		Text            string `json:"text"`
		Pattern         string `json:"pattern"`
		Group           int    `json:"group"`
		MaxMatches      int    `json:"max_matches"`
		CaseInsensitive bool   `json:"case_insensitive"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Text) == "" {
		return "", fmt.Errorf("text is required")
	}
	pattern := strings.TrimSpace(args.Pattern)
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if args.CaseInsensitive && !strings.HasPrefix(pattern, "(?i)") {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("compile pattern: %w", err)
	}

	maxMatches := args.MaxMatches
	if maxMatches <= 0 {
		maxMatches = 20
	}
	if maxMatches > 100 {
		maxMatches = 100
	}
	group := args.Group
	if group < 0 {
		group = 0
	}

	rawMatches := re.FindAllStringSubmatch(args.Text, maxMatches)
	matches := make([]string, 0, len(rawMatches))
	for _, match := range rawMatches {
		if group > 0 {
			if group < len(match) {
				matches = append(matches, match[group])
			}
			continue
		}
		if len(match) > 0 {
			matches = append(matches, match[0])
		}
	}

	out := map[string]any{
		"pattern":   args.Pattern,
		"group":     group,
		"count":     len(matches),
		"matches":   matches,
		"truncated":  len(rawMatches) >= maxMatches && len(matches) >= maxMatches,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal matches: %w", err)
	}
	return string(b), nil
}

func jsonExtract(_ context.Context, argsJSON string) (string, error) {
	var args struct {
		JSON string `json:"json"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.JSON) == "" {
		return "", fmt.Errorf("json is required")
	}
	if strings.TrimSpace(args.Path) == "" {
		return "", fmt.Errorf("path is required")
	}

	var doc any
	if err := json.Unmarshal([]byte(args.JSON), &doc); err != nil {
		return "", fmt.Errorf("parse json: %w", err)
	}
	value, ok := walkJSONPath(doc, args.Path)
	out := map[string]any{
		"path":  args.Path,
		"found": ok,
		"value": value,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(b), nil
}

func walkJSONPath(value any, path string) (any, bool) {
	current := value
	for _, token := range jsonPathTokenRe.FindAllString(path, -1) {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if strings.HasPrefix(token, "[") && strings.HasSuffix(token, "]") {
			idx, err := strconv.Atoi(strings.Trim(token, "[]"))
			if err != nil {
				return nil, false
			}
			slice, ok := current.([]any)
			if !ok || idx < 0 || idx >= len(slice) {
				return nil, false
			}
			current = slice[idx]
			continue
		}

		if mapValue, ok := current.(map[string]any); ok {
			var exists bool
			current, exists = mapValue[token]
			if !exists {
				return nil, false
			}
			continue
		}

		return nil, false
	}
	return current, true
}

func lineCount(text string) int {
	if text == "" {
		return 0
	}
	count := strings.Count(text, "\n") + 1
	if strings.HasSuffix(text, "\n") {
		count--
	}
	return count
}

func paragraphCount(text string) int {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	parts := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == '\n'
	})
	count := 0
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			count++
		}
	}
	if count == 0 {
		return 1
	}
	return count
}