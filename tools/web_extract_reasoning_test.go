package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// Reading a page does not think.
//
// The extraction asked nothing about reasoning, and qwen3.6 reasons by default:
// 45 extractions of 45 spent their whole reply budget thinking and came back
// empty, so web_research handed the investigation pages with nothing in them.
// Asked not to, 45 of 45 answered (5 November 2026).
func TestThePageExtractionAsksTheModelNotToThink(t *testing.T) {
	var sent []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var got map[string]any
		_ = json.Unmarshal(b, &got)
		sent = append(sent, got)
		w.Header().Set("Content-Type", "application/json")
		// The focused pass finds nothing, so the general summary runs too.
		content := "The report details enterprise AI adoption trends."
		if len(sent) == 1 {
			content = "__NO_RELEVANT_CONTENT__"
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":`+strconv.Quote(content)+`}}],"usage":{"total_tokens":1}}`)
	}))
	defer srv.Close()

	thinksByDefault := func(string) (llm.ModelFacts, bool) {
		return llm.ModelFacts{Thinking: llm.Thinking{Default: true, Optional: true}}, true
	}
	wf := NewWebFetchWithLLM(llm.NewClient(srv.URL, "k", "test").Catalog(thinksByDefault))
	body := []byte("<html><body><article><h1>AI Report</h1><p>" +
		strings.Repeat("Enterprise AI adoption is accelerating across industries and functions. ", 15) +
		"</p></article></body></html>")
	if _, err := wf.formatExtract(context.Background(), "HTTP 200 OK", "https://x.example/r", body, "the 2025 revenue"); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(sent) != 2 {
		t.Fatalf("the page went to the model %d times, want the extraction and the general summary", len(sent))
	}
	for i, got := range sent {
		r, _ := got["reasoning"].(map[string]any)
		if r == nil || r["enabled"] != false {
			t.Errorf("call %d reading the page sent reasoning %v, want {enabled: false}: a model that thinks by "+
				"default spends the reply budget thinking and returns nothing", i+1, got["reasoning"])
		}
	}
}
