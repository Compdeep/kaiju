package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// What web_fetch promises a caller, whatever it is fetching.
//
// The other web tests cover particular readers — the PDF route, the summariser
// fallback, the plugin reader. These cover the contract underneath all of them:
// which schemes are refused, what each format mode returns, that a 4xx is
// reported as a status rather than swallowed as a failure, that headers and a
// POST body are sent, and that the fetched url is stamped into the payload.
//
// That last one is a cross-package dependency. A tool result carries the url it
// came from so a later stage can tell which of the urls a run surfaced were
// actually retrieved. Stop stamping it and nothing fails — the run simply
// reports every page as unread.

// ── Interface contract ────────────────────────────────────────────────────

func TestWebFetch_Interfaces(t *testing.T) {
	w := NewWebFetch()
	var _ toolapi.Tool = w
	var _ toolapi.Outputter = w
}

func TestWebFetch_NoLLMByDefault(t *testing.T) {
	w := NewWebFetch()
	if w.executor != nil {
		t.Error("NewWebFetch should not wire an LLM client")
	}
}

func TestWebFetch_ParametersSchemaIsValidJSON(t *testing.T) {
	w := NewWebFetch()
	var schema map[string]any
	if err := json.Unmarshal(w.Parameters(), &schema); err != nil {
		t.Fatalf("Parameters is not valid JSON: %v", err)
	}
	if got := schema["additionalProperties"]; got != false {
		t.Errorf("additionalProperties should be false (strict schema), got %v", got)
	}
	required, _ := schema["required"].([]any)
	hasURL := false
	for _, r := range required {
		if s, ok := r.(string); ok && s == "url" {
			hasURL = true
		}
	}
	if !hasURL {
		t.Errorf("`url` should be required")
	}
}

// ── URL validation ────────────────────────────────────────────────────────

func TestWebFetch_RejectsEmptyURL(t *testing.T) {
	w := NewWebFetch()
	_, err := w.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error on empty URL")
	}
	if !strings.Contains(err.Error(), "url is required") {
		t.Errorf("error should explain missing url, got: %v", err)
	}
}

func TestWebFetch_RejectsNonHTTPSchemes(t *testing.T) {
	w := NewWebFetch()
	cases := []string{
		"file:///etc/passwd",
		"ftp://example.com",
		"javascript:alert(1)", // foreign-word-ok: the attack string this filter must reject, verbatim
		"example.com",
		"",
	}
	for _, u := range cases {
		t.Run(u, func(t *testing.T) {
			_, err := w.Execute(context.Background(), map[string]any{"url": u})
			if err == nil {
				t.Errorf("expected reject for %q", u)
			}
		})
	}
}

func TestWebFetch_AcceptsHTTPAndHTTPS(t *testing.T) {
	// Spin up a local server so we don't make a real network call.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`<html><body><p>hello</p></body></html>`))
	}))
	defer srv.Close()

	w := NewWebFetch()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := w.Execute(ctx, map[string]any{"url": srv.URL, "format": "markdown"}); err != nil {
		t.Fatalf("http URL should be accepted, got %v", err)
	}
}

// ── Format routing ────────────────────────────────────────────────────────

func TestWebFetch_KeepsThePageAndSaysWhere(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><h1>Title</h1><p>Body</p></body></html>`))
	}))
	defer srv.Close()

	// What replaced the "raw" mode. There is no longer a way to ask for a whole
	// document inline — there never really was one, since it was cut at eight
	// kilobytes — so the whole document is written and the result says where.
	ws := t.TempDir()
	w := NewWebFetchIn(ws, nil, FetchLimits{})
	out, err := w.Execute(context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	result := fetchPayload(t, out)

	rel, _ := result["full_content_path"].(string)
	if rel == "" {
		t.Fatal("no path on the result, so a caller cannot read the page it just fetched")
	}
	kept, rerr := os.ReadFile(filepath.Join(ws, rel))
	if rerr != nil {
		t.Fatalf("the path does not lead to the page: %v", rerr)
	}
	if !strings.Contains(string(kept), "<h1>Title</h1>") {
		t.Errorf("the kept page is not the page that was fetched: %s", kept)
	}
	if n, _ := result["bytes"].(float64); int(n) != len(kept) {
		t.Errorf("bytes = %v, want %d", result["bytes"], len(kept))
	}
}

func TestWebFetch_TextFormatStripsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><h1>Title</h1><p>Body text here.</p></body></html>`))
	}))
	defer srv.Close()

	w := NewWebFetch()
	out, err := w.Execute(context.Background(), map[string]any{"url": srv.URL, "format": "text"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	result := fetchPayload(t, out)
	content, _ := result["content"].(string)
	if strings.Contains(content, "<h1>") {
		t.Errorf("text format should strip HTML tags, got: %s", content)
	}
	if !strings.Contains(content, "Title") || !strings.Contains(content, "Body text") {
		t.Errorf("text format should keep text content, got: %s", content)
	}
}

func TestWebFetch_DefaultsToMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><head><title>Article Title</title></head><body><article><h1>Article Heading</h1><p>Some article content here that is long enough for readability to identify it as the main article body and extract it cleanly.</p><p>More body text to give the parser something to work with.</p></article></body></html>`))
	}))
	defer srv.Close()

	w := NewWebFetch()
	out, err := w.Execute(context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	result := fetchPayload(t, out)
	if result["format"] != "markdown" {
		t.Errorf("default format = %v, want markdown", result["format"])
	}
}

func TestWebFetch_SummaryWithoutLLMFallsBackToMarkdown(t *testing.T) {
	// NewWebFetch (no executor) should fall back to markdown when summary
	// is requested.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><article><p>Some article content.</p></article></body></html>`))
	}))
	defer srv.Close()

	w := NewWebFetch()
	out, err := w.Execute(context.Background(), map[string]any{"url": srv.URL, "format": "summary"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	result := fetchPayload(t, out)
	if result["format"] != "markdown" {
		t.Errorf("summary without LLM should fall back to markdown, got format=%v", result["format"])
	}
}

// ── Error status handling ────────────────────────────────────────────────

func TestWebFetch_4xxStatusReturnedNotFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`<html><body>Not Found</body></html>`))
	}))
	defer srv.Close()

	w := NewWebFetch()
	out, err := w.Execute(context.Background(), map[string]any{"url": srv.URL})
	// 4xx is not a tool error — caller may still want the body.
	if err != nil {
		t.Errorf("4xx should not return error, got: %v", err)
	}
	if !strings.Contains(out, "404") {
		t.Errorf("output should mention status code, got: %s", out)
	}
}

func TestWebFetch_5xxStatusReturnedNotFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte(`Service unavailable`))
	}))
	defer srv.Close()

	w := NewWebFetch()
	out, err := w.Execute(context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Errorf("5xx should not return error, got: %v", err)
	}
	if !strings.Contains(out, "503") {
		t.Errorf("output should mention status code, got: %s", out)
	}
}

func TestWebFetch_ConnectionErrorReturnsError(t *testing.T) {
	// Bind to a port that nothing is listening on. Reserved port 1 is the
	// portable way to force an immediate connection refusal.
	w := NewWebFetch()
	_, err := w.Execute(context.Background(), map[string]any{"url": "http://127.0.0.1:1/"})
	if err == nil {
		t.Fatal("expected error on connection refused")
	}
	if !strings.Contains(err.Error(), "web_fetch") {
		t.Errorf("error should be tagged web_fetch:, got: %v", err)
	}
}

// ── Custom headers / methods ─────────────────────────────────────────────

func TestWebFetch_PassesCustomHeaders(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Write([]byte(`<html><body>ok</body></html>`))
	}))
	defer srv.Close()

	w := NewWebFetch()
	_, err := w.Execute(context.Background(), map[string]any{
		"url":     srv.URL,
		"format":  "markdown",
		"headers": map[string]any{"Authorization": "Bearer test123"},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if receivedAuth != "Bearer test123" {
		t.Errorf("server saw Authorization=%q, want Bearer test123", receivedAuth)
	}
}

func TestWebFetch_POSTSendsBody(t *testing.T) {
	var receivedBody []byte
	var receivedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`<html><body>ok</body></html>`))
	}))
	defer srv.Close()

	w := NewWebFetch()
	_, err := w.Execute(context.Background(), map[string]any{
		"url":    srv.URL,
		"format": "markdown",
		"method": "POST",
		"body":   `{"q":"data"}`,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if receivedMethod != "POST" {
		t.Errorf("method = %q, want POST", receivedMethod)
	}
	if string(receivedBody) != `{"q":"data"}` {
		t.Errorf("body = %q, want JSON payload", string(receivedBody))
	}
}

// ── HTML stripping helpers ───────────────────────────────────────────────

func TestStripHTML_RemovesScriptAndStyle(t *testing.T) {
	in := `<html><script>alert(1)</script><style>.x{}</style><p>visible</p></html>` // foreign-word-ok: JavaScript's own function, in the markup being stripped
	got := stripHTML(in)
	if strings.Contains(got, "alert") { // foreign-word-ok: checking the script above is gone
		t.Errorf("script content should be stripped, got: %s", got)
	}
	if strings.Contains(got, ".x{}") {
		t.Errorf("style content should be stripped, got: %s", got)
	}
	if !strings.Contains(got, "visible") {
		t.Errorf("body content should remain, got: %s", got)
	}
}

func TestStripHTML_DecodesEntities(t *testing.T) {
	in := `<p>a &amp; b &lt; c &gt; d</p>`
	got := stripHTML(in)
	if !strings.Contains(got, "a & b < c > d") {
		t.Errorf("entities should be decoded, got: %s", got)
	}
}

func TestStripHTML_CollapsesWhitespace(t *testing.T) {
	in := "<p>line one</p>\n\n\n   \n<p>line two</p>"
	got := stripHTML(in)
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("excessive whitespace should be collapsed, got: %q", got)
	}
}

// fetchPayload unwraps web_fetch's tool envelope and returns the payload the
// assertions below read. The tool now emits {kind,status,content,detail,data}
// so absence and failure are typed rather than inferred; its own fields
// (status, title, content, format) ride verbatim in data, which is where
// ${node.<id>.content} resolves to as well.
func fetchPayload(t *testing.T, out string) map[string]any {
	t.Helper()
	msg, ok := toolapi.ParseToolMessage(out)
	if !ok {
		t.Fatalf("output is not a tool envelope: %s", out)
	}
	var payload map[string]any
	if len(msg.Data) > 0 {
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			t.Fatalf("envelope data is not an object: %v", err)
		}
	}
	return payload
}

// TestWebFetch_StampsTheFetchedURL guards a cross-package dependency: the agent's
// grounding edge decides which search results have been READ by looking for the
// url in a page envelope's payload. If web_fetch stops stamping it, every URL a
// search found looks unread forever and the edge keeps telling the planner to
// fetch what it already fetched — with nothing failing to say so.
func TestWebFetch_StampsTheFetchedURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><h1>Title</h1><p>Body text here.</p></body></html>"))
	}))
	defer srv.Close()

	w := NewWebFetch()
	out, err := w.Execute(context.Background(), map[string]any{"url": srv.URL, "format": "markdown"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	payload := fetchPayload(t, out)
	if payload["url"] != srv.URL {
		t.Errorf("payload url = %v, want %q — the grounding edge cannot tell this page was read", payload["url"], srv.URL)
	}
}

// A page that parses but holds nothing readable is empty, not ok.
//
// This needed a Note from a reader before, and only the summary and document
// readers set one. A page fetched as markdown with an empty body therefore came
// back ok with no Content, which a later stage reads as a page it retrieved.
func TestWebFetch_PageWithNothingReadableIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>x</title></head><body></body></html>`))
	}))
	defer srv.Close()

	msg, err := NewWebFetch().ExecuteTyped(context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("web_fetch: %v", err)
	}
	if msg.Status != toolapi.StatusEmpty {
		t.Errorf("a page with nothing readable = %q, want empty — ok with no content "+
			"cannot be told apart from a page that was read", msg.Status)
	}
	if msg.Detail == "" {
		t.Error("an empty fetch should say why, and which URL it was")
	}
}

// The URL on every result is declared, and says which kind of URL it is.
//
// web.go sets url on every result while the declaration said, in capitals, that this
// tool produces no URLs — meaning none discovered on the page. So it was the one tool
// in either repository returning a field its own schema denied having, found by
// comparing produced keys against declared ones across all 63.
func TestWebFetchDeclaresTheURLItEchoes(t *testing.T) {
	schema := string(NewWebFetch().OutputSchema())
	fields, ok := toolapi.DeclaredPayloadFields(NewWebFetch().OutputSchema())
	if !ok {
		t.Fatal("web_fetch declares no payload")
	}
	found := false
	for _, f := range fields {
		if f == "url" {
			found = true
		}
	}
	if !found {
		t.Errorf("url is produced on every result and not declared: %v", fields)
	}
	// And the description still steers a planner away from chaining fetch to fetch.
	if !strings.Contains(schema, "not a link found on the page") {
		t.Errorf("the declaration does not say which kind of URL this is: %s", schema)
	}
}
