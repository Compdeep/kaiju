package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A search hit that is a PDF must be read through the registered binary decoder
// (the pdf plugin's seam), not fed to readability as HTML. A stub decoder stands
// in for the plugin; web_fetch should return the decoded text as ok content.
func TestWebFetch_BinaryDecoderRoutesPDF(t *testing.T) {
	toolapi.RegisterBinaryDecoder("application/pdf", func(b []byte) (string, error) {
		return "DECODED(" + string(b) + ")", nil
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4 fake"))
	}))
	defer srv.Close()

	out, err := NewWebFetch().Execute(context.Background(), map[string]any{"url": srv.URL})
	m := mustEnvelope(t, out, err)
	if m.Status != toolapi.StatusOK {
		t.Fatalf("pdf fetch status = %q, want ok (note=%q)", m.Status, m.Detail)
	}
	if !strings.Contains(m.Content, "DECODED(%PDF-1.4 fake)") {
		t.Fatalf("decoded PDF text not returned: %q", m.Content)
	}
}

// The metadata fallback recovers the crawler-facing summary (title + og/meta
// description) from a page whose body is otherwise empty — a JS shell.
func TestExtractMeta(t *testing.T) {
	body := `<html><head>
<title>Sovereign AI Report 2026</title>
<meta property="og:description" content="A deep look at sovereign AI infrastructure in Japan &amp; the enterprise agent market.">
</head><body><div id="app"></div></body></html>`
	meta := extractMeta(body)
	if !strings.Contains(meta, "Sovereign AI Report 2026") {
		t.Fatalf("title not extracted: %q", meta)
	}
	if !strings.Contains(meta, "sovereign AI infrastructure in Japan & the enterprise agent market") {
		t.Fatalf("og:description not extracted (or not unescaped): %q", meta)
	}
}

// An enabled reader plugin is the PRIMARY reader — web_fetch must use it for the
// page, not just when built-in readability comes back thin. Here readability WOULD
// extract the static body, but the registered plugin's content must win.
func TestFormatMarkdown_PluginIsPrimaryReader(t *testing.T) {
	toolapi.RegisterReaderFallback(func(context.Context, string) (string, error) {
		return strings.Repeat("Rendered by the reader plugin. ", 20), nil
	})
	defer toolapi.RegisterReaderFallback(nil) // unregister for other tests

	body := []byte("<html><body><article>" +
		strings.Repeat("Static boilerplate readability would grab. ", 20) +
		"</article></body></html>")
	out, err := NewWebFetchWithLLM(nil).formatMarkdown(context.Background(), "HTTP 200 OK", "https://x.example/spa", body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Content, "Rendered by the reader plugin") {
		t.Fatalf("enabled reader plugin must be the primary reader, got: %+v", out)
	}
	if strings.Contains(out.Content, "Static boilerplate") {
		t.Fatalf("built-in readability leaked despite an enabled plugin: %+v", out)
	}
}

func TestLooksLikePDFURL(t *testing.T) {
	cases := map[string]bool{
		"https://example.gov/reports/ai-2026.pdf": true,
		"https://example.gov/reports/ai-2026.PDF": true,
		"https://example.com/article":             false,
		"https://example.com/x.pdf?dl=1":          true,
	}
	for u, want := range cases {
		if got := looksLikePDFURL(u); got != want {
			t.Errorf("looksLikePDFURL(%q) = %v, want %v", u, got, want)
		}
	}
}
