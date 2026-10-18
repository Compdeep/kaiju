package agent

import (
	"context"
	"strings"
	"testing"
)

// Two runs that chose different skills and different intents are given the
// same prompt up to where their own choices begin. That shared start is what a
// model host can cache, and it has to hold the long parts — the rules and the
// tool index — or there is nothing worth caching.
func TestPlannerPrompt_SharesItsStartAcrossRuns(t *testing.T) {
	a := &Agent{skillGuidance: loadBuiltinSkills(nil)}
	g1, g2 := NewGraph(), NewGraph()
	g1.ActiveCards = []string{"data_retrieval"}
	g2.ActiveCards = []string{"system_operations"}

	p1 := a.executiveSystemPrompt(context.Background(), g1, nil, "", "observe", "")
	p2 := a.executiveSystemPrompt(context.Background(), g2, nil, "", "execute", "")

	cut := strings.Index(p1, "## Skill Guidance")
	if cut < 0 {
		t.Fatal("the prompt has no Skill Guidance section")
	}
	shared := p1[:cut]
	if !strings.HasPrefix(p2, shared) {
		t.Errorf("two runs differ before their skill guidance begins; the start cannot be cached")
	}
	if !strings.Contains(shared, "## Rules") {
		t.Error("the rules are not in the shared start")
	}
	for _, own := range []string{"Intent: **observe**", "### data_retrieval", "Current time:"} {
		if i := strings.Index(p1, own); i < cut {
			t.Errorf("%q belongs to this run and sits before the shared start ends (at %d, cut %d)", own, i, cut)
		}
	}
}
