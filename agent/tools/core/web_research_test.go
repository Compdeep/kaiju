package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWebResearch_EmptyQuery(t *testing.T) {
	if _, err := NewWebResearch(SearchConfig{}, nil).Execute(context.Background(), map[string]any{"query": "  "}); err == nil {
		t.Fatal("empty query should error")
	}
}

func TestWebResearch_Schema(t *testing.T) {
	w := NewWebResearch(SearchConfig{}, nil)
	if w.Name() != "web_research" {
		t.Fatalf("name = %q", w.Name())
	}
	if !json.Valid(w.Parameters()) || !strings.Contains(string(w.Parameters()), "max_sources") {
		t.Fatalf("bad Parameters: %s", w.Parameters())
	}
	if !json.Valid(w.OutputSchema()) || !strings.Contains(string(w.OutputSchema()), `"enum":["ok","empty","error"]`) {
		t.Fatalf("OutputSchema should be the envelope: %s", w.OutputSchema())
	}
}
