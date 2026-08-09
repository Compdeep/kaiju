package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/tools"
	agenttools "github.com/Compdeep/kaiju/agent/tools"
)

// groundingAgent has web_search registered with the schema that marks its url
// field as a handle. The reference collector reads schemas from the registry, so
// a tool that is not registered declares nothing and contributes nothing — which
// is the behaviour, not a test artefact.
func groundingAgent() *Agent {
	reg := tools.NewRegistry()
	reg.Replace(&fakeTool{
		name:   "web_search",
		params: json.RawMessage(`{}`),
		output: agenttools.EnvelopeSchema(`{"type":"object","properties":{"results":{"type":"array","items":{"type":"object","properties":{"url":{"type":"string","x-reference":"web_fetch.url"},"title":{"type":"string"}}}}}}`),
	}, "builtin")
	reg.Replace(&fakeTool{name: "web_fetch", params: json.RawMessage(`{}`)}, "builtin")
	return &Agent{registry: reg}
}

func searchNode(g *Graph, tag string, urls ...string) {
	results := make([]map[string]any, 0, len(urls))
	for _, u := range urls {
		results = append(results, map[string]any{"url": u, "title": "t"})
	}
	id := g.AddNode(&Node{Type: NodeTool, Tag: tag, ToolName: "web_search"})
	g.SetBody(id, toolMessageBody{msg: agenttools.ToolOK("search", "", map[string]any{"results": results})})
}

// collectGrounded harvests only URLs that actually came from a web_search result.
func TestCollectGrounded(t *testing.T) {
	g := NewGraph()
	searchNode(g, "s1", "https://real.example/a", "https://real.example/b")
	// a non-search ok node contributes nothing.
	pID := g.AddNode(&Node{Type: NodeTool, Tag: "page", ToolName: "web_fetch"})
	g.SetBody(pID, toolMessageBody{msg: agenttools.ToolOK("page", "content", nil)})

	got := groundingAgent().collectGrounded(g)
	set := map[string]bool{}
	for _, u := range got {
		set[u] = true
	}
	if len(got) != 2 || !set["https://real.example/a"] || !set["https://real.example/b"] {
		t.Fatalf("collectGrounded = %v, want exactly the two search URLs", got)
	}
}

// Clean gathering (no empty/failed nodes) → the edge skips entirely.
func TestGroundingEdge_CleanRunSkips(t *testing.T) {
	g := NewGraph()
	searchNode(g, "ok", "https://x/y")
	if grd := groundingAgent().groundingEdge(context.Background(), g, "req"); grd != "" {
		t.Fatalf("clean run should skip the edge, got %q", grd)
	}
}

// Safety property: a gap present + no light lane → the edge STILL emits a
// "## Grounding" block naming the only real URL and telling the planner to search
// for more — so the next step is never handed a blank slate to invent into.
func TestGroundingEdge_FailOpenToStructural(t *testing.T) {
	g := NewGraph()
	searchNode(g, "s", "https://real.example/a") // one real, grounded URL
	eID := g.AddNode(&Node{Type: NodeTool, Tag: "empty_search", ToolName: "web_search"})
	g.SetBody(eID, toolMessageBody{msg: agenttools.ToolEmpty("search", "no results")}) // the gap

	grd := groundingAgent().groundingEdge(context.Background(), g, "find 10 sources")
	if !strings.HasPrefix(grd, "## Grounding") {
		t.Fatalf("expected a ## Grounding block, got: %q", grd)
	}
	if !strings.Contains(grd, "https://real.example/a") {
		t.Fatalf("block must list the unfetched grounded URL:\n%s", grd)
	}
	if !strings.Contains(grd, "FETCH") {
		t.Fatalf("with an unfetched grounded URL, the block must push to FETCH it (not search more):\n%s", grd)
	}
}

// With NO grounded URL (the searches returned nothing), the edge tells the planner
// to broaden the search — not to fetch or invent.
func TestGroundingEdge_ReSearchWhenNothingGrounded(t *testing.T) {
	g := NewGraph()
	eID := g.AddNode(&Node{Type: NodeTool, Tag: "empty", ToolName: "web_search"})
	g.SetBody(eID, toolMessageBody{msg: agenttools.ToolEmpty("search", "no results")})
	grd := groundingAgent().groundingEdge(context.Background(), g, "find sources")
	if !strings.HasPrefix(grd, "## Grounding") || !strings.Contains(grd, "broaden the search") {
		t.Fatalf("empty-grounded should tell the planner to broaden the search, got:\n%s", grd)
	}
}

// The conclusion floor: a run that found real URLs but read NONE of them must not
// conclude yet — the floor returns deterministic web_fetch remediation steps for
// the top unread URLs (capped). This is the search-only "verified and live" case.
func TestConclusionFloor_GroundingUnread(t *testing.T) {
	g := NewGraph()
	searchNode(g, "s1", "https://a.example/1", "https://a.example/2", "https://a.example/3")
	steps, label := groundingAgent().conclusionFloor(g, 2) // cap at 2
	if len(steps) != 2 {
		t.Fatalf("expected 2 fetch steps (capped from 3), got %d", len(steps))
	}
	for _, s := range steps {
		if s.Tool != "web_fetch" {
			t.Fatalf("floor remediation must be web_fetch (grounded), got %q", s.Tool)
		}
		if u, _ := s.Params["url"].(string); u == "" {
			t.Fatalf("fetch step missing url param: %+v", s.Params)
		}
	}
	if label == "" {
		t.Fatal("floor should return a human label for the worklog")
	}
}

// Once ANY source was read, the floor is met — trust the reflector, don't force more.
func TestConclusionFloor_MetWhenSomethingRead(t *testing.T) {
	g := NewGraph()
	searchNode(g, "s1", "https://a.example/1", "https://a.example/2")
	fetchedNode(g, "f1", "https://a.example/1") // read one
	if steps, _ := groundingAgent().conclusionFloor(g, 6); steps != nil {
		t.Fatalf("floor is met once anything is read, got %d steps", len(steps))
	}
}

// No search at all → no grounded URLs → floor never fires (self-limiting to web work).
func TestConclusionFloor_NoSearchNoFloor(t *testing.T) {
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "bash", ToolName: "bash"})
	g.SetBody(id, toolMessageBody{msg: agenttools.ToolOK("bash", "done", nil)})
	if steps, _ := groundingAgent().conclusionFloor(g, 6); steps != nil {
		t.Fatalf("no search → no floor, got %d steps", len(steps))
	}
}

// With a light lane, the edge runs the generator and prepends its reframe.
func TestGroundingEdge_GeneratesFromLLM(t *testing.T) {
	const note = "GROUNDED: https://real.example/a\nNOT YET GROUNDED: an OECD report — search for it\nNEXT: broaden the search"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":`+strconv.Quote(note)+`}}],"usage":{"total_tokens":1}}`)
	}))
	defer srv.Close()

	a := &Agent{executor: llm.NewClient(srv.URL, "k", "test-light")}
	g := NewGraph()
	searchNode(g, "s", "https://real.example/a")
	fID := g.AddNode(&Node{Type: NodeTool, Tag: "bad", ToolName: "web_fetch"})
	g.SetBody(fID, toolMessageBody{msg: agenttools.ToolFail("page", "HTTP 404", nil)}) // the gap

	grd := a.groundingEdge(context.Background(), g, "find sources")
	if !strings.HasPrefix(grd, "## Grounding") {
		t.Fatalf("expected the ## Grounding header, got: %q", grd)
	}
	if !strings.Contains(grd, "NOT YET GROUNDED: an OECD report") {
		t.Fatalf("generated reframe not prepended:\n%s", grd)
	}
}
