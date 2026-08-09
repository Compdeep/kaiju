//go:build plugin_remote

package remote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// captureHost records what the bridge contributes at activation.
type captureHost struct {
	tools  []toolapi.Tool
	reader func(ctx context.Context, rawURL string) (string, error)
}

func (h *captureHost) Workspace() string                                          { return "" }
func (h *captureHost) AddTool(t toolapi.Tool)                                     { h.tools = append(h.tools, t) }
func (h *captureHost) RegisterBinaryDecoder(string, func([]byte) (string, error)) {}
func (h *captureHost) RegisterReaderFallback(fn func(ctx context.Context, rawURL string) (string, error)) {
	h.reader = fn
}

// A manifest tool marked "reader":true must (a) still be added as a callable tool
// AND (b) register web_fetch's ReaderFallback, which invokes the host and returns
// its extracted content — the wiring that makes an enabled reader plugin the
// automatic reader for every fetch.
func TestBridge_ReaderToolRegistersFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/plugins" {
			_, _ = w.Write([]byte(`{"plugins":[{"name":"webreader","skill":"","tools":[{"name":"web_read","description":"read","parameters":{},"impact":"observe","reader":true}]}]}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/invoke/") {
			_, _ = w.Write([]byte(`{"kind":"page","status":"ok","content":"RENDERED CONTENT","data":{"url":"x"}}`))
		}
	}))
	defer srv.Close()
	os.Setenv("KAIJU_PLUGIN_HOST", srv.URL)
	defer os.Unsetenv("KAIJU_PLUGIN_HOST")

	h := &captureHost{}
	bridge{}.Register(h)

	if len(h.tools) != 1 {
		t.Fatalf("expected the reader tool to still be callable, got %d tools", len(h.tools))
	}
	if h.reader == nil {
		t.Fatal("a reader-marked tool must register a ReaderFallback")
	}
	txt, err := h.reader(context.Background(), "https://x.example")
	if err != nil {
		t.Fatalf("fallback error: %v", err)
	}
	if txt != "RENDERED CONTENT" {
		t.Fatalf("fallback returned %q, want the host's extracted content", txt)
	}
}
