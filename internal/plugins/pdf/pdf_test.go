//go:build plugin_pdf

package pdf

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
	"github.com/Compdeep/kaiju/internal/plugins"
)

// TestPluginRegistersTool confirms the plugin exposes exactly the pdf_extract
// tool and that it satisfies the tool interface (observe impact, has a schema).
func TestPluginRegistersTool(t *testing.T) {
	ts := plugin{}.Tools(plugins.Deps{})
	if len(ts) != 1 || ts[0].Name() != "pdf_extract" {
		t.Fatalf("Tools() = %v, want one pdf_extract", ts)
	}
	tool := ts[0]
	if tool.Impact(nil) != agenttools.ImpactObserve {
		t.Errorf("Impact = %d, want observe (%d)", tool.Impact(nil), agenttools.ImpactObserve)
	}
	if len(tool.Parameters()) == 0 || !strings.Contains(string(tool.Parameters()), "path") {
		t.Errorf("Parameters missing 'path': %s", tool.Parameters())
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
