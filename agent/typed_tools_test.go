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
	agenttools "github.com/Compdeep/kaiju/agent/tools"
	"github.com/Compdeep/kaiju/tools"
)

// typedResult runs a tool through the typed path the dispatcher uses and puts
// the result on a graph the way the scheduler does.
func typedResult(t *testing.T, tool agenttools.Tool, params map[string]any) (*agent.Graph, agenttools.ToolMessage) {
	t.Helper()
	typed, ok := tool.(agenttools.TypedExecutor)
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

	if msg.Type != "page" || msg.Status != agenttools.StatusOK {
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

// An empty file has to reach the coverage statement, which is the reason for
// reporting it as empty rather than ok. Checked through FrameCoverage, the same
// call an application makes, rather than through the unexported collector.
func TestFileRead_EmptyFileReachesTheCoverageStatement(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.conf"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	g, msg := typedResult(t, tools.NewFileRead(dir), map[string]any{"path": "app.conf"})
	if msg.Status != agenttools.StatusEmpty {
		t.Fatalf("status = %q, want empty", msg.Status)
	}

	a := &agent.Agent{}
	// The coverage statement is prepended to the stage's user prompt; the role
	// prompt only gains the hook telling the model what to do with it.
	framed := a.FrameCoverage(context.Background(), g, agent.NewStagePrompts("role", "user"))
	if !strings.Contains(framed.User, "app.conf") {
		t.Fatalf("the coverage statement should name the empty file, got:\n%s", framed.User)
	}
}

// A file with content is not a gap, so the same call must stay silent about it.
func TestFileRead_ContentIsNotReportedAsAGap(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.conf"), []byte("listen = 8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g, msg := typedResult(t, tools.NewFileRead(dir), map[string]any{"path": "app.conf"})
	if msg.Status != agenttools.StatusOK || !strings.Contains(msg.Content, "8080") {
		t.Fatalf("envelope = status %q content %q", msg.Status, msg.Content)
	}

	a := &agent.Agent{}
	framed := a.FrameCoverage(context.Background(), g, agent.NewStagePrompts("role", "user"))
	if strings.Contains(framed.User, "app.conf") {
		t.Fatalf("a file that had content is not a gap, got:\n%s", framed.User)
	}
}

// web_search's envelope is what the grounding edge reads to tell a URL a real
// search returned from one the model recalled. It matches on kind "search",
// status ok, and a results list in the payload, so the migration has to preserve
// all three.
//
// The edge only speaks when something else in the run came back empty — with
// nothing missing there is no reason to press the model about where a URL came
// from. So the run here has a search that worked and a read that found nothing,
// which is when it matters.
func TestWebSearch_ResultsReachTheGroundingEdge(t *testing.T) {
	results := []map[string]string{
		{"title": "First", "url": "https://example.test/one", "snippet": "a"},
		{"title": "Second", "url": "https://example.test/two", "snippet": "b"},
	}
	g := agent.NewGraph()
	found := g.AddNode(&agent.Node{Type: agent.NodeTool, Tag: "search", ToolName: "web_search"})
	g.SetBody(found, agent.NewToolBody(
		agenttools.ToolOK("search", "", map[string]any{"query": "q", "results": results})))

	missing := g.AddNode(&agent.Node{Type: agent.NodeTool, Tag: "readconf", ToolName: "file_read"})
	g.SetBody(missing, agent.NewToolBody(agenttools.ToolEmpty("text", "the file is empty: app.conf")))

	// The engine reads which fields are handles from the tool's own output
	// schema, so web_search has to be registered for its url field to be seen.
	// A tool that is not registered declares nothing, which is the behaviour
	// rather than a limitation of the test.
	a := newTestAgent(t)
	_ = a.Registry().Register(tools.NewWebSearch())

	framed := a.FrameGrounding(context.Background(), g, agent.NewStagePrompts("role", "user"))
	for _, want := range []string{"https://example.test/one", "https://example.test/two"} {
		if !strings.Contains(framed.User, want) {
			t.Errorf("the grounding edge should list %s as searched but unread, got:\n%s", want, framed.User)
		}
	}
}

// A search that found nothing is both a coverage gap and the case the grounding
// edge exists for: there is no real URL yet, so the next move is to search
// again rather than to name one from memory.
func TestWebSearch_NoResultsIsAGapAndBlocksCitation(t *testing.T) {
	g := agent.NewGraph()
	id := g.AddNode(&agent.Node{Type: agent.NodeTool, Tag: "search", ToolName: "web_search"})
	g.SetBody(id, agent.NewToolBody(agenttools.ToolEmpty("search", "no reachable results for this query")))

	a := &agent.Agent{}
	covered := a.FrameCoverage(context.Background(), g, agent.NewStagePrompts("role", "user"))
	if !strings.Contains(covered.User, "no reachable results") {
		t.Errorf("an empty search should be a coverage gap, got:\n%s", covered.User)
	}
	grounded := a.FrameGrounding(context.Background(), g, agent.NewStagePrompts("role", "user"))
	if !strings.Contains(grounded.User, "No URL has come from a real search") {
		t.Errorf("with no grounded URL the edge should say so, got:\n%s", grounded.User)
	}
}

// sysinfo always succeeds and carries its whole result in the payload, so the
// fields have to stay addressable and it must never look like a gap.
func TestSysinfo_FieldsResolveAndItIsNotAGap(t *testing.T) {
	g, msg := typedResult(t, tools.NewSysinfo("/tmp/ws"), map[string]any{})

	if msg.Status != agenttools.StatusOK || msg.Content != "" {
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

	a := &agent.Agent{}
	if framed := a.FrameCoverage(context.Background(), g, agent.NewStagePrompts("role", "user")); framed.User != "user" {
		t.Errorf("sysinfo always succeeds and is never a gap, got:\n%s", framed.User)
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
