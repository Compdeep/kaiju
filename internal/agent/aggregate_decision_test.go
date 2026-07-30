package agent

import (
	"testing"

	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
)

// decideAutoAggMode is the answer-writer choice. The behavioral change this guards:
// a COMPLEX query aggregates by default and the reflector can only skip when there's
// no usable evidence — it can no longer short-circuit a real research run with a
// terse summary.
func TestDecideAutoAggMode(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name                             string
		hasCompute, complex, hasEvidence bool
		reflectorWants                   *bool
		want                             int
	}{
		{"compute always aggregates", true, false, false, nil, 1},
		{"complex + evidence → reasoning synthesis", false, true, true, nil, 2},
		{"complex + NO evidence → reflector's honest verdict", false, true, false, nil, 0},
		{"simple + reflector wants it → aggregate", false, false, true, &yes, 2},
		{"simple + reflector done → skip", false, false, true, &no, 0},
		{"simple + reflector nil → skip", false, false, true, nil, 0},
		// The key fix: a complex query aggregates even when the reflector said "skip".
		{"complex overrides reflector-skip", false, true, true, &no, 2},
	}
	for _, c := range cases {
		if got, _ := decideAutoAggMode(c.hasCompute, c.complex, c.hasEvidence, c.reflectorWants); got != c.want {
			t.Errorf("%s: got agg_mode=%d, want %d", c.name, got, c.want)
		}
	}
}

// hasUsableEvidence is true iff some tool node returned ok; runFanout counts
// resolved tool nodes (the structural complexity backstop).
func TestUsableEvidenceAndFanout(t *testing.T) {
	a := &Agent{}

	// A run with only empty/failed fetches has no usable evidence.
	g1 := NewGraph()
	e := g1.AddNode(&Node{Type: NodeTool, Tag: "s", ToolName: "web_search"})
	g1.SetBody(e, toolMessageBody{msg: agenttools.ToolEmpty("search", "no results")})
	f := g1.AddNode(&Node{Type: NodeTool, Tag: "f", ToolName: "web_fetch"})
	g1.SetBody(f, toolMessageBody{msg: agenttools.ToolFail("page", "HTTP 404", nil)})
	if a.hasUsableEvidence(g1) {
		t.Error("empty+failed only → hasUsableEvidence should be false")
	}
	if got := a.runFanout(g1); got != 2 {
		t.Errorf("runFanout = %d, want 2", got)
	}

	// One ok result flips usable to true.
	ok := g1.AddNode(&Node{Type: NodeTool, Tag: "ok", ToolName: "web_fetch"})
	g1.SetBody(ok, toolMessageBody{msg: agenttools.ToolOK("page", "real content", nil)})
	if !a.hasUsableEvidence(g1) {
		t.Error("an ok result → hasUsableEvidence should be true")
	}
	if got := a.runFanout(g1); got != 3 {
		t.Errorf("runFanout = %d, want 3", got)
	}

	if a.hasUsableEvidence(nil) || a.runFanout(nil) != 0 {
		t.Error("nil graph must be safe (false / 0)")
	}
}
