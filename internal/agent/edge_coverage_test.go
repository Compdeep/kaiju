package agent

import (
	"fmt"
	"testing"

	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
)

// collectGaps is the content-agnostic code half of the coverage edge: it must
// flag empty/error tool bodies and failed nodes, and leave successful ones alone.
func TestCollectGaps(t *testing.T) {
	g := NewGraph()

	okID := g.AddNode(&Node{Type: NodeTool, Tag: "fetch_ok", ToolName: "web_fetch"})
	g.SetBody(okID, toolMessageBody{msg: agenttools.ToolOK("page", "content", nil)})

	emID := g.AddNode(&Node{Type: NodeTool, Tag: "search_x", ToolName: "web_search"})
	g.SetBody(emID, toolMessageBody{msg: agenttools.ToolEmpty("search", "no reachable results")})

	erID := g.AddNode(&Node{Type: NodeTool, Tag: "fetch_bad", ToolName: "web_fetch"})
	g.SetBody(erID, toolMessageBody{msg: agenttools.ToolFail("page", "HTTP 404", nil)})

	failID := g.AddNode(&Node{Type: NodeTool, Tag: "bash_x", ToolName: "bash"})
	g.SetError(failID, fmt.Errorf("boom"))

	gaps := (&Agent{}).collectGaps(g)
	if len(gaps) != 3 {
		t.Fatalf("collectGaps = %d, want 3 (empty + error + failed, NOT ok): %+v", len(gaps), gaps)
	}
	for _, gp := range gaps {
		if gp.Tag == "fetch_ok" {
			t.Fatalf("a successful tool must not be reported as a gap")
		}
	}
}

// A clean run (no empty/error/failed nodes) must skip the edge entirely so the
// common path pays nothing — the LLM lane is never touched.
func TestCoverageEdge_CleanRunSkips(t *testing.T) {
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "ok", ToolName: "web_fetch"})
	g.SetBody(id, toolMessageBody{msg: agenttools.ToolOK("page", "content", nil)})

	if cov := (&Agent{}).coverageEdge(nil, g, "some evidence"); cov != "" {
		t.Fatalf("clean run should skip the edge (return \"\"), got %q", cov)
	}
}
