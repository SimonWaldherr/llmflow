package util

import (
	"strings"
	"testing"
)

func TestPrettyJSONMap(t *testing.T) {
	result := PrettyJSON(map[string]any{"key": "value"})
	if !strings.Contains(result, "\"key\"") || !strings.Contains(result, "\"value\"") {
		t.Fatalf("unexpected output: %q", result)
	}
	if !strings.Contains(result, "\n") {
		t.Fatal("expected multiline pretty JSON output")
	}
}

func TestPrettyJSONNil(t *testing.T) {
	if result := PrettyJSON(nil); result != "null" {
		t.Fatalf("expected null, got %q", result)
	}
}

func TestPrettyJSONInvalid(t *testing.T) {
	if result := PrettyJSON(make(chan int)); result != "{}" {
		t.Fatalf("expected fallback {}, got %q", result)
	}
}

func TestPrettyJSONSlice(t *testing.T) {
	result := PrettyJSON([]string{"a", "b"})
	if !strings.Contains(result, "\"a\"") || !strings.Contains(result, "\"b\"") {
		t.Fatalf("unexpected output: %q", result)
	}
}
