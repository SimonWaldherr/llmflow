package web

import (
	"testing"
	"time"
)

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

func TestParseSuggestResponseWithRepair_UsesRepairGenerator(t *testing.T) {
	raw := "Here is your config:\n```json\n{\"system_prompt\":\"broken\",}\n```"
	called := false
	got, err := parseSuggestResponseWithRepair(raw, 30*time.Second, func(systemPrompt, userMsg string, timeout time.Duration) (string, error) {
		called = true
		if systemPrompt != suggestRepairSystemPrompt {
			t.Fatalf("unexpected repair prompt")
		}
		if timeout != 10*time.Second {
			t.Fatalf("unexpected repair timeout: %v", timeout)
		}
		return `{"system_prompt":"fixed","response_field":"result"}`, nil
	})
	if err != nil {
		t.Fatalf("parseSuggestResponseWithRepair returned error: %v", err)
	}
	if !called {
		t.Fatal("expected repair generator to be called")
	}
	if got.SystemPrompt != "fixed" {
		t.Fatalf("system_prompt=%q, want fixed", got.SystemPrompt)
	}
	if got.ResponseField != "result" {
		t.Fatalf("response_field=%q, want result", got.ResponseField)
	}
}
