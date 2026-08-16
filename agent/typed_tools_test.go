package agent_test

// These run the real tools. An earlier version built envelopes by hand that
// looked like what web_fetch and file_read produce, which proved nothing about
// either tool — the same test passed whatever the tool did. Running the tool is
// the point, since the migration changed the tools and not the mechanism.
//
// The package is agent_test rather than agent: internal/tools imports agent, so
// a test inside agent cannot import the tools. Everything used here is exported,
// which also means the test only leans on what an application can lean on.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/agent/toolapi"
	"github.com/Compdeep/kaiju/tools"
)

// typedResult runs a tool through the typed path the dispatcher uses and puts
// the result on a graph the way the scheduler does.
func typedResult(t *testing.T, tool toolapi.Tool, params map[string]any) (*agent.Graph, toolapi.ToolMessage) {
	t.Helper()
	typed, ok := tool.(toolapi.TypedExecutor)
	if !ok {
		t.Fatalf("%s does not implement TypedExecutor", tool.Name())
	}
	msg, err := typed.ExecuteTyped(context.Background(), params)
	if err != nil {
		t.Fatalf("%s: %v", tool.Name(), err)
	}
	g := agent.NewGraph()
	id := g.AddNode(&agent.Node{Type: agent.NodeTool, Tag: tool.Name(), ToolName: tool.Name()})
	g.SetBody(id, agent.NewToolBody(msg))
	return g, msg
}

// A page far larger than the old 4096-character result cap, and larger than
// web_fetch's own 8192-character bound for text. The tool's bound is a clean cut
// with a marker; the dispatcher's was a byte splice through the JSON, which left
// the node with no status and no resolvable fields. Typed, only the tool's bound
// applies.
func TestWebFetch_LargePageArrivesWhole(t *testing.T) {
	page := strings.Repeat("the quick brown fox. ", 2000) // ~42 KB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><head><title>Report</title></head><body><article><p>" + page + "</p></article></body></html>"))
	}))
	defer srv.Close()

	g, msg := typedResult(t, tools.NewWebFetch(), map[string]any{"url": srv.URL, "format": "text"})

	if msg.Type != "page" || msg.Status != toolapi.StatusOK {
		t.Fatalf("envelope = kind %q status %q", msg.Type, msg.Status)
	}
	// The evidence a model reads is the page, not the envelope around it.
	evidence := g.ResolvedResultsSoFar()
	if len(evidence) != 1 {
		t.Fatalf("want one resolved node, got %d", len(evidence))
	}
	for _, got := range evidence {
		// Comfortably past the 4096 the dispatcher used to impose on this tool.
		// The exact size is set by two later bounds that apply to every tool —
		// web_fetch's own 8192 for the text format, then TruncateEvidence at
		// 8000 — so this asserts the dispatcher's cut is gone, not a size.
		if len(got) <= 4096 {
			t.Errorf("evidence is %d chars — no larger than the cap typing was meant to lift", len(got))
		}
		if !strings.Contains(got, "truncated") {
			t.Error("a page that was cut should say so")
		}
		if strings.Contains(got, `"kind":"page"`) {
			t.Error("the envelope reached the model instead of the page text")
		}
	}
	// And the payload is still addressable, which splicing destroyed.
	if v, ok := agent.NewToolBody(msg).Field("status"); !ok || !strings.Contains(v.(string), "200") {
		t.Errorf("${step.N.status} = (%v, %v), want the HTTP status", v, ok)
	}
	// format=text carries no title — only the summary format sets one — so the
	// field that must survive here is the one this format produces.
	if v, ok := agent.NewToolBody(msg).Field("format"); !ok || v != "text" {
		t.Errorf("${step.N.format} = (%v, %v), want text", v, ok)
	}
}

// An empty file says so in its envelope, and a file with content does not.
//
// These used to go on to check that the empty one reached the coverage
// statement a later stage was given. That statement is gone — see the commit
// that removed the coverage and grounding edges — so what is left is the
// envelope, which is what every consumer reads.
func TestFileRead_AnEmptyFileSaysSo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.conf"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	_, msg := typedResult(t, tools.NewFileRead(dir), map[string]any{"path": "app.conf"})
	if msg.Status != toolapi.StatusEmpty {
		t.Fatalf("status = %q, want empty", msg.Status)
	}
}

func TestFileRead_ContentIsNotEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.conf"), []byte("listen = 8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, msg := typedResult(t, tools.NewFileRead(dir), map[string]any{"path": "app.conf"})
	if msg.Status != toolapi.StatusOK || !strings.Contains(msg.Content, "8080") {
		t.Fatalf("envelope = status %q content %q", msg.Status, msg.Content)
	}
}

// sysinfo carries its whole result in the payload, so the fields have to stay
// addressable by a later step.
func TestSysinfo_FieldsResolve(t *testing.T) {
	_, msg := typedResult(t, tools.NewSysinfo("/tmp/ws"), map[string]any{})

	if msg.Status != toolapi.StatusOK || msg.Content != "" {
		t.Fatalf("envelope = status %q content %q — the payload is the readable form here", msg.Status, msg.Content)
	}
	body := agent.NewToolBody(msg)
	for _, field := range []string{"hostname", "os", "arch", "cpus"} {
		if v, ok := body.Field(field); !ok || v == nil {
			t.Errorf("${step.N.%s} = (%v, %v), want a value", field, v, ok)
		}
	}
	if v, ok := body.Field("cwd"); !ok || v != "/tmp/ws" {
		t.Errorf("${step.N.cwd} = (%v, %v), want the workspace", v, ok)
	}
}

// newTestAgent builds an agent with an empty registry and nothing else, which
// is all these tests need — every call under test reads the graph and the
// registry.
func newTestAgent(t *testing.T) *agent.Agent {
	t.Helper()
	a, err := agent.New(agent.Config{PathConfig: agent.PathConfig{
		Workspace: t.TempDir(), MetadataDir: t.TempDir(), DataDir: t.TempDir(),
	}})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return a
}
