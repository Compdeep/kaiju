package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A step that worked but whose output the group reviewer judged not useful.
//
// applyGroupReview used to set such a step failed, so a fetch whose page loaded
// fine, or a command that exited 0, read in the trace and to the reflector as a
// call that broke (run b09e2b84: n19's own summary said "page ok"). The step
// keeps its state; the verdict is recorded beside it.

func notUsefulGraph(t *testing.T) (*Graph, string, string) {
	t.Helper()
	g := NewGraph()
	worked := g.AddNode(&Node{Type: NodeTool, Tag: "fetch_jupiter", ToolName: "web_fetch"})
	g.SetBody(worked, toolMessageBody{msg: toolapi.ToolOK("page", "Jupiter: two and a half times the mass", map[string]any{"content": "Jupiter: two and a half times the mass"})})
	broke := g.AddNode(&Node{Type: NodeTool, Tag: "fetch_helio", ToolName: "web_fetch"})
	g.SetError(broke, errString("invalid URL"))
	(&Agent{}).applyGroupReview(g, []*Node{g.Get(worked), g.Get(broke)}, groupReview{Unusable: []unusableReply{
		{Tag: "fetch_jupiter", Action: "give_up", Why: "no heliocentric distance in AU"},
		{Tag: "fetch_helio", Action: "give_up", Why: "a sentence was passed as the URL"},
	}})
	return g, worked, broke
}

type errString string

func (e errString) Error() string { return string(e) }

func TestAStepThatWorkedKeepsItsStateWhenJudgedNotUseful(t *testing.T) {
	g, worked, broke := notUsefulGraph(t)
	if s := g.Get(worked).State; s != StateResolved {
		t.Errorf("the step that worked is %v, want resolved", s)
	}
	if s := g.Get(broke).State; s != StateFailed {
		t.Errorf("the step that broke is %v, want failed", s)
	}
	info := g.SnapshotNode(worked)
	if info.ErrType != "not_useful" || !strings.Contains(info.Error, "no heliocentric distance") {
		t.Errorf("trace shows %q / %q, want not_useful with the reason", info.ErrType, info.Error)
	}
}

// Its output is not evidence: it is left out of the resolved results the
// reflector and the answer read, and named with its reason instead.
func TestANotUsefulStepIsNotEvidence(t *testing.T) {
	g, _, _ := notUsefulGraph(t)
	for label := range g.ResolvedResultsSoFar() {
		if strings.Contains(label, "fetch_jupiter") {
			t.Errorf("ResolvedResultsSoFar still carries %s", label)
		}
	}
	cur, earlier := g.ResolvedResultsByRound()
	for label := range cur {
		if strings.Contains(label, "fetch_jupiter") {
			t.Errorf("ResolvedResultsByRound still carries %s", label)
		}
	}
	for label := range earlier {
		if strings.Contains(label, "fetch_jupiter") {
			t.Errorf("ResolvedResultsByRound (earlier) still carries %s", label)
		}
	}
	text, err := (&nodeReturnsSource{}).Load(g, &Trigger{}, &Agent{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "### Judged not useful") || !strings.Contains(text, "no heliocentric distance") {
		t.Errorf("evidence does not name the step judged not useful:\n%s", text)
	}
	if strings.Contains(text, "two and a half times") {
		t.Errorf("the output judged not useful reached the evidence:\n%s", text)
	}
}

// Enbarr's verdict gate reads EvidenceStats: a step judged not useful is not
// evidence, and is counted as it was before, with the failures.
func TestANotUsefulStepIsNotCountedAsEvidence(t *testing.T) {
	g, _, _ := notUsefulGraph(t)
	tools, failed, withResult := g.EvidenceStats()
	if tools != 2 || failed != 2 || withResult != 0 {
		t.Errorf("EvidenceStats = %d tools, %d failed, %d with result; want 2, 2, 0", tools, failed, withResult)
	}
}

// What a stage is given for the step says it was judged not useful.
func TestTheStepResultSaysItWasJudgedNotUseful(t *testing.T) {
	g, worked, _ := notUsefulGraph(t)
	for _, r := range g.StepResults(-1) {
		if r.NodeID != worked {
			continue
		}
		if c := stepResultContent(r); !strings.Contains(c, `"not_useful"`) || !strings.Contains(c, "no heliocentric distance") {
			t.Errorf("step result reads %s, want the verdict beside it", c)
		}
		return
	}
	t.Fatal("the step is missing from StepResults")
}
