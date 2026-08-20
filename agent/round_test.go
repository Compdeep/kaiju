package agent

import (
	"strings"
	"testing"
)

// A run that replans several times used to present every step it had ever taken
// as equally current, so a problem solved in the first round went on being
// described as the situation in the fifth — and the stage reading it went on
// solving it again.

func resolvedToolNode(g *Graph, tag, result string) string {
	id := g.AddNode(&Node{Type: NodeTool, Tag: tag, ToolName: "probe"})
	g.SetState(id, StateResolved)
	g.Get(id).Result = result
	return id
}

func TestAddNode_StampsTheRoundInForce(t *testing.T) {
	g := NewGraph()
	first := resolvedToolNode(g, "first", "a")

	g.BeginRound()
	second := resolvedToolNode(g, "second", "b")

	if got := g.Get(first).Round; got != 0 {
		t.Errorf("first node round = %d, want 0", got)
	}
	if got := g.Get(second).Round; got != 1 {
		t.Errorf("second node round = %d, want 1", got)
	}
	if got := g.Round(); got != 1 {
		t.Errorf("graph round = %d, want 1", got)
	}
}

func TestResolvedResultsByRound_SeparatesEarlierFromNow(t *testing.T) {
	g := NewGraph()
	resolvedToolNode(g, "old_attempt", "the first attempt's output")
	g.BeginRound()
	resolvedToolNode(g, "new_attempt", "the current attempt's output")

	current, earlier := g.ResolvedResultsByRound()

	if len(current) != 1 || len(earlier) != 1 {
		t.Fatalf("current=%d earlier=%d, want 1 and 1", len(current), len(earlier))
	}
	for label := range current {
		if !strings.Contains(label, "new_attempt") {
			t.Errorf("current holds %q", label)
		}
	}
	for label := range earlier {
		if !strings.Contains(label, "old_attempt") {
			t.Errorf("earlier holds %q", label)
		}
	}
}

// The whole chain: what a stage actually reads has to put the two apart.
func TestStepOutcomes_PutsEarlierRoundsUnderTheirOwnHeading(t *testing.T) {
	g := NewGraph()
	resolvedToolNode(g, "old_attempt", "x")
	g.BeginRound()
	resolvedToolNode(g, "new_attempt", "y")

	out, err := (&stepOutcomesSource{}).Load(g, nil, nil, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	heading := strings.Index(out, "EARLIER ROUNDS")
	if heading < 0 {
		t.Fatalf("no heading separates the rounds:\n%s", out)
	}
	oldAt, newAt := strings.Index(out, "old_attempt"), strings.Index(out, "new_attempt")
	if oldAt < heading {
		t.Errorf("a step from an earlier round is written above the heading, so it reads as current:\n%s", out)
	}
	if newAt > heading {
		t.Errorf("this round's step is written under the earlier-rounds heading:\n%s", out)
	}
}

// A run that never replans must read exactly as it did before.
func TestStepOutcomes_SingleRoundIsUnchanged(t *testing.T) {
	g := NewGraph()
	resolvedToolNode(g, "only_step", "x")

	out, err := (&stepOutcomesSource{}).Load(g, nil, nil, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if strings.Contains(out, "EARLIER ROUNDS") {
		t.Errorf("a run with one round grew a history section:\n%s", out)
	}
	if !strings.Contains(out, "only_step") {
		t.Errorf("the step is missing:\n%s", out)
	}
}
