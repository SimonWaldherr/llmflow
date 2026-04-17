package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestTextStatsTool(t *testing.T) {
	tool := NewTextStatsTool()
	out, err := tool.Execute(context.Background(), `{"text":"Hello world\nSecond line"}`)
	if err != nil {
		t.Fatalf("text_stats returned error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if int(got["words"].(float64)) != 4 {
		t.Fatalf("words = %v, want 4", got["words"])
	}
	if int(got["lines"].(float64)) != 2 {
		t.Fatalf("lines = %v, want 2", got["lines"])
	}
}

func TestRegexExtractTool(t *testing.T) {
	tool := NewRegexExtractTool()
	out, err := tool.Execute(context.Background(), `{"text":"Order A12 and B34","pattern":"[A-Z][0-9]{2}"}`)
	if err != nil {
		t.Fatalf("regex_extract returned error: %v", err)
	}
	var got struct {
		Matches []string `json:"matches"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if len(got.Matches) != 2 || got.Matches[0] != "A12" || got.Matches[1] != "B34" {
		t.Fatalf("unexpected matches: %#v", got.Matches)
	}
}

func TestJSONExtractTool(t *testing.T) {
	tool := NewJSONExtractTool()
	out, err := tool.Execute(context.Background(), `{"json":"{\"customer\":{\"address\":{\"city\":\"Berlin\"}}}","path":"customer.address.city"}`)
	if err != nil {
		t.Fatalf("json_extract returned error: %v", err)
	}
	var got struct {
		Found bool   `json:"found"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if !got.Found || got.Value != "Berlin" {
		t.Fatalf("unexpected result: %#v", got)
	}
}
