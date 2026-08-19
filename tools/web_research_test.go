package tools

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

// The sources list says where an answer came from, not the page again.
//
// Each source used to carry its extracted text in the payload while the readable
// half carried the same text trimmed to 4000 characters — so one call put every page
// in the result twice. Measured on one query: 11,479 characters of text beside
// 20,862 of payload, which the 8000-character evidence cap then cut head-and-tail
// across the pair.
func TestWebResearchSourcesDoNotRepeatThePages(t *testing.T) {
	// Read the source's own properties rather than searching the whole schema: the
	// envelope declares a content field of its own, which is the result's readable
	// half and is meant to be there.
	var envelope struct {
		Properties struct {
			Data struct {
				Properties struct {
					Sources struct {
						Items struct {
							Properties map[string]json.RawMessage `json:"properties"`
						} `json:"items"`
					} `json:"sources"`
				} `json:"properties"`
			} `json:"data"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(NewWebResearch(SearchConfig{}, nil).OutputSchema(), &envelope); err != nil {
		t.Fatalf("output schema: %v", err)
	}
	source := envelope.Properties.Data.Properties.Sources.Items.Properties
	if len(source) == 0 {
		t.Fatal("the schema declares no source fields, so this checks nothing")
	}

	if _, repeats := source["content"]; repeats {
		t.Error("a source still declares the page's text, which the result already carries")
	}
	for _, field := range []string{"chars", "trimmed", "status", "url"} {
		if _, present := source[field]; !present {
			t.Errorf("a source does not declare %q", field)
		}
	}
}
