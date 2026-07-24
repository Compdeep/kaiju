package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFilterReachable covers the grounding gate: dead URLs (404) and unreachable
// hosts are dropped, duplicates are collapsed, and the 405-keep rule keeps a
// source whose server refuses HEAD but would serve GET.
func TestFilterReachable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/gone", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	mux.HandleFunc("/nohead", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed) // 405 — but GET works
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/blocked", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }) // 403 — kept
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ws := NewWebSearch()
	in := []searchResult{
		{Title: "ok", URL: srv.URL + "/ok"},
		{Title: "gone", URL: srv.URL + "/gone"},           // 404 → drop
		{Title: "nohead", URL: srv.URL + "/nohead"},       // 405 → keep
		{Title: "blocked", URL: srv.URL + "/blocked"},     // 403 → keep (not proof it's dead)
		{Title: "dup", URL: srv.URL + "/ok/"},             // duplicate of /ok → drop
		{Title: "dead", URL: "http://127.0.0.1:1/nope"},   // connection refused → drop
	}

	out := ws.filterReachable(context.Background(), in)

	got := map[string]bool{}
	for _, r := range out {
		got[r.Title] = true
	}
	// ok, nohead, blocked survive; gone/dup/dead are gone.
	if len(out) != 3 || !got["ok"] || !got["nohead"] || !got["blocked"] {
		t.Fatalf("filterReachable kept %v, want {ok, nohead, blocked}", titles(out))
	}
}

func TestNormalizeURL_DedupKey(t *testing.T) {
	cases := [][2]string{
		{"https://Example.com/Page/", "example.com/Page"},   // host lowercased, trailing slash trimmed
		{"http://example.com/Page", "example.com/Page"},      // scheme ignored → collides with above host+path
		{"https://example.com/p?q=1", "example.com/p?q=1"},   // query kept
		{"https://example.com/p#frag", "example.com/p"},       // fragment ignored
	}
	for _, c := range cases {
		if got := normalizeURL(c[0]); got != c[1] {
			t.Errorf("normalizeURL(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func titles(rs []searchResult) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Title
	}
	return out
}
