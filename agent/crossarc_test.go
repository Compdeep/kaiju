package agent

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// arcGraph is a run that fetched something, then replanned.
func arcGraph(t *testing.T) (*Graph, string) {
	t.Helper()
	g := NewGraph()
	prior := g.AddNode(&Node{Type: NodeTool, Tag: "fetch_tam", ToolName: "web_fetch"})
	g.SetBody(prior, toolMessageBody{msg: toolapi.ToolOK("page", "the page",
		json.RawMessage(`{"value":"$8,391.1 million"}`))})
	g.BeginRound()
	return g, prior
}

// A name is the only handle that survives a replan: positions restart with the
// plan, tags do not. A reference to a step that already ran resolves to the
// node holding its value, rather than being left as literal text for a tool.
//
// This is the run that started it: a re-plan's compute step read
// ${step.fetch_tam.content}, fetch_tam having run in the arc before. The
// reference resolved to nothing, three corrections could not move it, and the
// run ended with no answer.
func TestCrossArc_ANameReachesAStepThatAlreadyRan(t *testing.T) {
	g, prior := arcGraph(t)
	steps := []PlanStep{{Tool: "compute", Tag: "calc"}}
	params := map[string]any{"tam": "${step.fetch_tam.value}"}

	deps := rewriteStepTemplates(params, []string{"n2"}, "n2", steps, nil, g)

	if got := params["tam"]; got != "${node."+prior+".value}" {
		t.Fatalf("the reference did not reach the finished step: %v", got)
	}
	if len(deps) != 1 || deps[0] != prior {
		t.Errorf("the finished step was not recorded as an input: %v", deps)
	}
}

// The same, for a reference written as a value rather than inside a string.
func TestCrossArc_ADeclaredReferenceReachesItToo(t *testing.T) {
	g, prior := arcGraph(t)
	n := &Node{ID: "n2", Type: NodeCompute, Tag: "calc", Params: map[string]any{
		"tam": map[string]any{"step": "fetch_tam", "field": "value"},
	}}
	deps := resolveDeclaredRefs(n, []string{"n2"}, []PlanStep{{Tool: "compute", Tag: "calc"}}, g)

	if got := n.Params["tam"]; got != "${node."+prior+".value}" {
		t.Fatalf("the declared reference did not reach the finished step: %v", got)
	}
	if len(deps) != 1 || deps[0] != prior {
		t.Errorf("deps = %v, want [%s]", deps, prior)
	}
}

// The plan being written wins. A planner that names a step it just wrote means
// that step — reaching back would hand it the older run's value instead, which
// is a wrong answer rather than a failure.
func TestCrossArc_ThePlanBeingWrittenWins(t *testing.T) {
	g := NewGraph()
	old := g.AddNode(&Node{Type: NodeTool, Tag: "search", ToolName: "web_search"})
	g.SetBody(old, toolMessageBody{msg: toolapi.ToolOK("s", "old", json.RawMessage(`{"n":1}`))})
	g.BeginRound()

	steps := []PlanStep{{Tool: "web_search", Tag: "search"}, {Tool: "compute", Tag: "calc"}}
	params := map[string]any{"v": "${step.search.n}"}
	rewriteStepTemplates(params, []string{"nNEW", "nCALC"}, "nCALC", steps, nil, g)

	if got := params["v"]; got != "${node.nNEW.n}" {
		t.Errorf("a name in this plan resolved to the earlier run's step: %v (want nNEW, not %s)", got, old)
	}
}

// A name nothing has ever held stays unresolved and is reported. Rewriting it
// into a template pointing nowhere would turn a named fault into a silent one.
func TestCrossArc_AnUnknownNameIsStillLeftAlone(t *testing.T) {
	g, _ := arcGraph(t)
	params := map[string]any{"v": "${step.never_existed.field}"}
	deps := rewriteStepTemplates(params, []string{"n2"}, "n2", []PlanStep{{Tool: "compute", Tag: "c"}}, nil, g)

	if got := params["v"]; got != "${step.never_existed.field}" {
		t.Errorf("an unknown name was rewritten to %v", got)
	}
	if len(deps) != 0 {
		t.Errorf("an unknown name produced a dependency: %v", deps)
	}
}

// Validation must not refuse what wiring will resolve. The two disagreeing
// would fail a plan that was about to work.
func TestCrossArc_ValidationAcceptsWhatWiringResolves(t *testing.T) {
	g, _ := arcGraph(t)
	steps := []PlanStep{{Tool: "compute", Tag: "calc", Params: map[string]any{
		"tam": "${step.fetch_tam.value}",
	}}}
	if errs := validatePlanReferencesIn(steps, nil, g); len(errs) != 0 {
		t.Errorf("a reference the wiring resolves was reported as a fault: %v", errs)
	}
	// And a name nothing ran is still a fault, with the run in hand.
	bad := []PlanStep{{Tool: "compute", Tag: "calc", Params: map[string]any{
		"x": "${step.never_ran.value}",
	}}}
	if errs := validatePlanReferencesIn(bad, nil, g); len(errs) == 0 {
		t.Error("a name nothing in this run ever held was accepted")
	}
}

// The newest wins when a name was used twice, and a step that RESOLVED wins
// over a newer one that did not — a failed step has no value to read.
func TestCrossArc_PrefersTheNewestThatActuallyProduced(t *testing.T) {
	g := NewGraph()
	first := g.AddNode(&Node{Type: NodeTool, Tag: "search", ToolName: "web_search"})
	g.SetBody(first, toolMessageBody{msg: toolapi.ToolOK("s", "first", json.RawMessage(`{"n":1}`))})

	g.BeginRound()
	second := g.AddNode(&Node{Type: NodeTool, Tag: "search", ToolName: "web_search"})
	g.SetBody(second, toolMessageBody{msg: toolapi.ToolOK("s", "second", json.RawMessage(`{"n":2}`))})

	if id, round, ok := g.FinishedStep("search"); !ok || id != second || round != 1 {
		t.Errorf("newest did not win: id=%s round=%d (want %s, round 1)", id, round, second)
	}

	// Now a newer one that failed: the older, resolved step is the useful answer.
	g.BeginRound()
	third := g.AddNode(&Node{Type: NodeTool, Tag: "search", ToolName: "web_search"})
	g.SetError(third, errNoValue{})
	if id, _, ok := g.FinishedStep("search"); !ok || id != second {
		t.Errorf("a failed newer step shadowed the resolved one: got %s, want %s", id, second)
	}
}

// The run grafts nodes of its own — "reflect", "operator", "debug_1". They are
// not the planner's vocabulary, and a reference must not reach one.
func TestCrossArc_DoesNotReachTheRunsOwnNodes(t *testing.T) {
	g := NewGraph()
	g.SetBody(g.AddNode(&Node{Type: NodeReflection, Tag: "reflect"}),
		toolMessageBody{msg: toolapi.ToolOK("r", "reflected", nil)})
	g.BeginRound()
	if _, _, ok := g.FinishedStep("reflect"); ok {
		t.Error("a reference can reach the run's own reflection node")
	}
}

type errNoValue struct{}

func (errNoValue) Error() string { return "nothing came back" }

// Reaching back is loud on purpose.
//
// It is right often enough to be worth doing — the planner has that step's
// result in front of it and is wiring to what it read. It is ALSO how a plan
// that forgot to add a step still resolves, quietly reading the older value
// instead of producing a fresh one. That is a wrong answer rather than a
// failure, and this line is the only place a reader can see it happened.
func TestCrossArc_ReachingBackSaysSoInTheLog(t *testing.T) {
	g, prior := arcGraph(t)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	params := map[string]any{"tam": "${step.fetch_tam.value}"}
	rewriteStepTemplates(params, []string{"n2"}, "n2", []PlanStep{{Tool: "compute", Tag: "calc"}}, nil, g)

	out := buf.String()
	if !strings.Contains(out, "warning") {
		t.Errorf("reaching back into an earlier round was silent, or was reported as something other than a warning: %q", out)
	}
	for _, want := range []string{"fetch_tam", prior, "round 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("the line does not say %q, so a reader cannot tell which value was used: %q", want, out)
		}
	}
	// It must name the thing that goes wrong, or nobody reading it knows why it matters.
	if !strings.Contains(out, "missing the step") {
		t.Errorf("the line does not say what a reader should suspect: %q", out)
	}
}
