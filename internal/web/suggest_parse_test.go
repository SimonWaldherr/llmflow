package web

import "testing"

func TestParseSuggestResponse_WithMarkdownWrapper(t *testing.T) {
	raw := "Sure, here is the config:\n```json\n{\"system_prompt\":\"x\",\"response_field\":\"r\"}\n```"
	got, err := parseSuggestResponse(raw)
	if err != nil {
		t.Fatalf("parseSuggestResponse returned error: %v", err)
	}
	if got.SystemPrompt != "x" {
		t.Fatalf("system_prompt=%q, want x", got.SystemPrompt)
	}
	if got.ResponseField != "r" {
		t.Fatalf("response_field=%q, want r", got.ResponseField)
	}
}

func TestParseSuggestResponse_RepairsUnescapedNewlineInString(t *testing.T) {
	raw := "{\"system_prompt\":\"line1\nline2\",\"response_field\":\"r\"}"
	got, err := parseSuggestResponse(raw)
	if err != nil {
		t.Fatalf("parseSuggestResponse returned error: %v", err)
	}
	if got.SystemPrompt != "line1\nline2" {
		t.Fatalf("system_prompt=%q, want multiline value", got.SystemPrompt)
	}
}

func TestParseSuggestResponse_NoJSONObject(t *testing.T) {
	_, err := parseSuggestResponse("no json here")
	if err == nil {
		t.Fatal("expected error for missing JSON object")
	}
}
