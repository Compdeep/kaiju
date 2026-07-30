package tools

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/internal/agent/llm"
)

// When a focused summary misses (the model returns the no-content sentinel) but
// readability DID extract real page text, web_fetch must fall back to a general
// summary instead of discarding the whole page — recovering content-bearing
// sources (analyst/report pages) that just didn't phrase the exact focus asked for.
func TestFormatSummary_FocusMissFallsBackToGeneral(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		content := "The report details enterprise AI adoption trends and spending growth."
		if calls == 1 {
			content = "__NO_RELEVANT_CONTENT__" // the focused pass finds no exact match
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":`+strconv.Quote(content)+`}}],"usage":{"total_tokens":1}}`)
	}))
	defer srv.Close()

	wf := NewWebFetchWithLLM(llm.NewClient(srv.URL, "k", "test"))
	body := []byte("<html><body><article><h1>AI Report</h1><p>" +
		strings.Repeat("Enterprise AI adoption is accelerating across industries and functions. ", 15) +
		"</p></article></body></html>")

	out, err := wf.formatSummary(context.Background(), "HTTP 200 OK", "https://x.example/report", body, "the exact 2025 revenue figure in USD")
	if err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Fatalf("expected a general-summary retry after the focus miss, got %d LLM call(s)", calls)
	}
	if !strings.Contains(out, "enterprise AI adoption trends") {
		t.Fatalf("focus-miss on a content-bearing page should return the general summary, got: %s", out)
	}
	if strings.Contains(out, "did not contain the requested information") {
		t.Fatalf("content-bearing page was discarded instead of general-summarized: %s", out)
	}
}

// If even the general pass finds nothing (a truly empty/JS page), it still reports
// "no content" — the fallback recovers real pages, it does not fabricate.
func TestFormatSummary_GeneralAlsoEmptyStillReportsNoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"__NO_RELEVANT_CONTENT__"}}],"usage":{"total_tokens":1}}`)
	}))
	defer srv.Close()

	wf := NewWebFetchWithLLM(llm.NewClient(srv.URL, "k", "test"))
	body := []byte("<html><body><article><p>" + strings.Repeat("Generic filler text with no facts here. ", 15) + "</p></article></body></html>")
	out, _ := wf.formatSummary(context.Background(), "HTTP 200 OK", "https://x.example/empty", body, "revenue")
	if !strings.Contains(out, "did not contain the requested information") {
		t.Fatalf("both passes empty → should report no content, got: %s", out)
	}
}
