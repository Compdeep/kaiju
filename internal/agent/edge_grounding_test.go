package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/internal/agent/llm"
	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
)

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

	got := (&Agent{}).collectGrounded(g)
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
	if grd := (&Agent{}).groundingEdge(context.Background(), g, "req"); grd != "" {
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

	grd := (&Agent{}).groundingEdge(context.Background(), g, "find 10 sources")
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
	grd := (&Agent{}).groundingEdge(context.Background(), g, "find sources")
	if !strings.HasPrefix(grd, "## Grounding") || !strings.Contains(grd, "broaden the search") {
		t.Fatalf("empty-grounded should tell the planner to broaden the search, got:\n%s", grd)
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
