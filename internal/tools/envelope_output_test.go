package tools

import (
	"os"
	"path/filepath"
	"testing"

	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
)

// mustEnvelope asserts a tool's Execute output is a well-formed ToolMessage.
func mustEnvelope(t *testing.T, out string, err error) agenttools.ToolMessage {
	t.Helper()
	if err != nil {
		t.Fatalf("Execute errored: %v", err)
	}
	m, ok := agenttools.ParseToolMessage(out)
	if !ok {
		t.Fatalf("output is not a ToolMessage envelope: %s", out)
	}
	return m
}

func TestSysinfo_EmitsEnvelope(t *testing.T) {
	out, err := NewSysinfo().Execute(nil, nil)
	m := mustEnvelope(t, out, err)
	if m.Kind != "sysinfo" || m.Status != agenttools.StatusOK {
		t.Fatalf("sysinfo envelope = kind %q status %q", m.Kind, m.Status)
	}
	if len(m.Data) == 0 {
		t.Fatalf("sysinfo should carry structured data")
	}
}

func TestFileRead_EmitsTextEnvelope(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := NewFileRead(dir).Execute(nil, map[string]any{"path": "f.txt"})
	m := mustEnvelope(t, out, err)
	if m.Kind != "text" || m.Status != agenttools.StatusOK || m.Content != "hello world" {
		t.Fatalf("file_read envelope = kind %q status %q content %q", m.Kind, m.Status, m.Content)
	}
}

func TestFileList_EmitsListingEnvelope(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	out, err := NewFileList(dir).Execute(nil, map[string]any{"path": "."})
	m := mustEnvelope(t, out, err)
	if m.Kind != "listing" || m.Status != agenttools.StatusOK {
		t.Fatalf("file_list envelope = kind %q status %q", m.Kind, m.Status)
	}
	if len(m.Data) == 0 {
		t.Fatalf("file_list should carry entries in data")
	}
}

// The audit's silent-miss / no-match cases must now surface as StatusEmpty — the
// framing signal the coverage edge reads.
func TestEnvList_NoMatchIsEmpty(t *testing.T) {
	out, err := NewEnvList().Execute(nil, map[string]any{"filter": "ZZ_NO_SUCH_VAR_XYZ_"})
	m := mustEnvelope(t, out, err)
	if m.Status != agenttools.StatusEmpty {
		t.Fatalf("env_list with no matches should be StatusEmpty, got %q", m.Status)
	}
}
