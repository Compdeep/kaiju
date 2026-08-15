package agent

import (
	"fmt"
	"testing"
)

// TestEvidenceStats_CountsToolOutcomes verifies the deterministic tool-outcome
// counter the outcome gate relies on: only terminal NodeTool nodes count, failed
// vs resolved-with-a-real-result are tallied, and non-tool nodes are ignored.
func TestEvidenceStats_CountsToolOutcomes(t *testing.T) {
	g := NewGraph()
	g.SetResult(g.AddNode(&Node{Type: NodeTool}), `{"procs":[]}`)           // evidence
	g.SetError(g.AddNode(&Node{Type: NodeTool}), fmt.Errorf("unreachable")) // failed
	g.SetResult(g.AddNode(&Node{Type: NodeTool}), "   ")                    // resolved but empty
	g.SetResult(g.AddNode(&Node{Type: NodeCompute}), "reasoning")           // not a tool → ignored

	tool, failed, withResult := g.EvidenceStats()
	if tool != 3 || failed != 1 || withResult != 1 {
		t.Errorf("EvidenceStats = (%d,%d,%d), want (3,1,1)", tool, failed, withResult)
	}
}
