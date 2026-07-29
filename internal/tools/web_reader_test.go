package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
)

// A search hit that is a PDF must be read through the registered binary decoder
// (the pdf plugin's seam), not fed to readability as HTML. A stub decoder stands
// in for the plugin; web_fetch should return the decoded text as ok content.
func TestWebFetch_BinaryDecoderRoutesPDF(t *testing.T) {
	agenttools.RegisterBinaryDecoder("application/pdf", func(b []byte) (string, error) {
		return "DECODED(" + string(b) + ")", nil
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4 fake"))
	}))
	defer srv.Close()

	out, err := NewWebFetch().Execute(context.Background(), map[string]any{"url": srv.URL})
	m := mustEnvelope(t, out, err)
	if m.Status != agenttools.StatusOK {
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
