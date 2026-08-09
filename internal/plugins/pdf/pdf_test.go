//go:build plugin_pdf

package pdf

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
	"github.com/Compdeep/kaiju/internal/plugins"
)

// captureHost is a test double for plugins.Host that records everything a plugin
// contributes through Register — both its tools and its seams.
type captureHost struct {
	ws       string
	tools    []toolapi.Tool
	decoders map[string]func([]byte) (string, error)
}

var _ plugins.Host = (*captureHost)(nil)

func (h *captureHost) Workspace() string      { return h.ws }
func (h *captureHost) AddTool(t toolapi.Tool) { h.tools = append(h.tools, t) }
func (h *captureHost) RegisterBinaryDecoder(mime string, fn func([]byte) (string, error)) {
	if h.decoders == nil {
		h.decoders = map[string]func([]byte) (string, error){}
	}
	h.decoders[mime] = fn
}
func (h *captureHost) RegisterReaderFallback(func(context.Context, string) (string, error)) {}

// TestPluginRegistersToolAndSeam confirms Register contributes BOTH the
// pdf_extract tool (planner-callable, observe impact, has a schema) AND the
// application/pdf decoder seam (called by core web_fetch, not the planner).
func TestPluginRegistersToolAndSeam(t *testing.T) {
	h := &captureHost{ws: t.TempDir()}
	plugin{}.Register(h)

	if len(h.tools) != 1 || h.tools[0].Name() != "pdf_extract" {
		t.Fatalf("Register added tools %v, want one pdf_extract", h.tools)
	}
	tool := h.tools[0]
	if tool.Impact(nil) != toolapi.ImpactObserve {
		t.Errorf("Impact = %d, want observe (%d)", tool.Impact(nil), toolapi.ImpactObserve)
	}
	if len(tool.Parameters()) == 0 || !strings.Contains(string(tool.Parameters()), "path") {
		t.Errorf("Parameters missing 'path': %s", tool.Parameters())
	}

	if _, ok := h.decoders["application/pdf"]; !ok {
		t.Fatalf("Register did not register an application/pdf decoder: %v", h.decoders)
	}
}

// TestExecuteValidation covers the input-validation and sandbox paths without
// needing a real PDF on disk.
func TestExecuteValidation(t *testing.T) {
	tool := &extractTool{workspace: t.TempDir()}

	if _, err := tool.Execute(context.Background(), map[string]any{}); err == nil {
		t.Error("missing path should error")
	}

	// A path outside the workspace must be rejected.
	if _, err := tool.Execute(context.Background(), map[string]any{"path": "/etc/hostname"}); err == nil {
		t.Error("path escaping workspace should error")
	}
}

// TestExtractSamplePDF runs the tool against a real text-based PDF only if the
// KAIJU_PDF_SAMPLE env var points at one; it self-skips otherwise so the suite
// stays hermetic.
func TestExtractSamplePDF(t *testing.T) {
	sample := os.Getenv("KAIJU_PDF_SAMPLE")
	if sample == "" {
		t.Skip("set KAIJU_PDF_SAMPLE=/path/to.pdf to run")
	}
	tool := &extractTool{workspace: filepath.Dir(sample)}
	out, err := tool.Execute(context.Background(), map[string]any{"path": filepath.Base(sample)})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "PDF: ") {
		t.Fatalf("missing header: %.60q", out)
	}
	t.Logf("extracted %d chars", len(out))
}
