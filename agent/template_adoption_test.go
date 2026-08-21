package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// What resolving a reference has to do, gathered in one place.
//
// A second implementation of this job existed elsewhere and is being retired.
// Everything its tests insisted on is asked of this one, so nothing it had
// checked stops being checked. Where the two disagreed, the disagreement is
// recorded below as a decision rather than left to be discovered later by
// whatever the resolver is holding up.

func graphWithDep(t *testing.T, result string, state NodeState) (*Graph, string) {
	t.Helper()
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, ToolName: "upstream"})
	if state == StateFailed {
		g.SetError(id, fmt.Errorf("upstream failed"))
		// SetError leaves the result empty; a failed step that still produced
		// output is the case being covered, so put it back.
		g.mu.Lock()
		g.nodes[id].Result = result
		g.mu.Unlock()
	} else {
		g.SetResult(id, result)
	}
	return g, id
}

func TestParametersWithNoTemplatesAreLeftAlone(t *testing.T) {
	g := NewGraph()
	n := &Node{Params: map[string]any{"x": "literal"}}
	n.ID = g.AddNode(n)
	if err := substituteTemplates(n, g, nil); err != nil {
		t.Errorf("params with nothing to substitute reported %v", err)
	}
	if n.Params["x"] != "literal" {
		t.Errorf("params were changed: %v", n.Params)
	}
}

func TestANodeWithNoParametersIsLeftAlone(t *testing.T) {
	if err := substituteTemplates(&Node{}, NewGraph(), nil); err != nil {
		t.Errorf("a node with no params reported %v", err)
	}
}

func TestABareReferenceKeepsTheObject(t *testing.T) {
	g, dep := graphWithDep(t, `{"results":[{"url":"https://x"},{"url":"https://y"}]}`, StateResolved)
	n := &Node{Params: map[string]any{"data": "${node." + dep + "}"}}
	n.ID = g.AddNode(n)
	if err := substituteTemplates(n, g, nil); err != nil {
		t.Fatalf("substituteTemplates: %v", err)
	}
	got, ok := n.Params["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is %T, want the object the dependency produced", n.Params["data"])
	}
	if _, has := got["results"]; !has {
		t.Errorf("data = %v, want it to carry results", got)
	}
}

func TestAPathReadsAFieldOutOfTheDependency(t *testing.T) {
	g, dep := graphWithDep(t, `{"url":"https://example.com/a"}`, StateResolved)
	n := &Node{Params: map[string]any{"target": "${node." + dep + ".url}"}}
	n.ID = g.AddNode(n)
	if err := substituteTemplates(n, g, nil); err != nil {
		t.Fatalf("substituteTemplates: %v", err)
	}
	if n.Params["target"] != "https://example.com/a" {
		t.Errorf("target = %v", n.Params["target"])
	}
}

func TestSeveralReferencesInOneStringAreAllSubstituted(t *testing.T) {
	g, dep := graphWithDep(t, `{"name":"alpha","port":8080}`, StateResolved)
	n := &Node{Params: map[string]any{
		"cmd": "ssh ${node." + dep + ".name}:${node." + dep + ".port}",
	}}
	n.ID = g.AddNode(n)
	if err := substituteTemplates(n, g, nil); err != nil {
		t.Fatalf("substituteTemplates: %v", err)
	}
	if n.Params["cmd"] != "ssh alpha:8080" {
		t.Errorf("cmd = %v, want ssh alpha:8080", n.Params["cmd"])
	}
}

func TestAReferenceToANodeThatIsNotThereIsAnError(t *testing.T) {
	g := NewGraph()
	n := &Node{Params: map[string]any{"x": "${node.nonexistent.field}"}}
	n.ID = g.AddNode(n)
	err := substituteTemplates(n, g, nil)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want it to say the node was not found", err)
	}
}

func TestAReferenceToADependencyWithNoOutputIsAnError(t *testing.T) {
	g := NewGraph()
	dep := g.AddNode(&Node{})
	g.SetResult(dep, "")
	n := &Node{Params: map[string]any{"x": "${node." + dep + ".f}"}}
	n.ID = g.AddNode(n)
	err := substituteTemplates(n, g, nil)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %v, want it to say the result was empty", err)
	}
}

// A step that failed still produced output, and the next step often wants it —
// a shell that exited non-zero has its stderr in the result.
func TestAFailedDependencyThatProducedOutputStillResolves(t *testing.T) {
	g, dep := graphWithDep(t, `{"err":"oops","exit":1}`, StateFailed)
	n := &Node{Params: map[string]any{"data": "${node." + dep + "}"}}
	n.ID = g.AddNode(n)
	if err := substituteTemplates(n, g, nil); err != nil {
		t.Fatalf("a failed dependency with output reported %v", err)
	}
	if got := n.Params["data"].(map[string]any)["err"]; got != "oops" {
		t.Errorf("data.err = %v, want oops", got)
	}
}

// A field asked of a dependency that returned prose.
//
// This engine forgives it: the miss is logged and the whole result is injected
// in place of the field, on the reading that a tool returning prose where a
// field was expected is the tool's bug and should not stop a working pipeline.
// The other tree failed the step instead. Both are defensible and this one is
// the engine's, so this one stands.
func TestAFieldAskedOfProseInjectsTheWholeResult(t *testing.T) {
	g, dep := graphWithDep(t, "just a string, not JSON", StateResolved)
	n := &Node{Params: map[string]any{"x": "${node." + dep + ".field}"}}
	n.ID = g.AddNode(n)
	if err := substituteTemplates(n, g, nil); err != nil {
		t.Fatalf("substituteTemplates: %v", err)
	}
	if n.Params["x"] != "just a string, not JSON" {
		t.Errorf("x = %v, want the whole result", n.Params["x"])
	}
}

// The other half of it: no field asked for, so prose is the answer.
func TestProseWithNoFieldAskedForResolves(t *testing.T) {
	g, dep := graphWithDep(t, "raw output bytes", StateResolved)
	n := &Node{Params: map[string]any{"x": "${node." + dep + "}"}}
	n.ID = g.AddNode(n)
	if err := substituteTemplates(n, g, nil); err != nil {
		t.Fatalf("a bare reference to prose reported %v", err)
	}
	if n.Params["x"] != "raw output bytes" {
		t.Errorf("x = %v, want the prose unchanged", n.Params["x"])
	}
}

func TestSeveralFieldsOfOneDependencyAllResolve(t *testing.T) {
	g, dep := graphWithDep(t, `{"a":"alpha","b":"beta","c":"gamma"}`, StateResolved)
	n := &Node{Params: map[string]any{
		"x": "${node." + dep + ".a}",
		"y": "${node." + dep + ".b}",
		"z": "${node." + dep + ".c}",
	}}
	n.ID = g.AddNode(n)
	if err := substituteTemplates(n, g, nil); err != nil {
		t.Fatalf("substituteTemplates: %v", err)
	}
	if n.Params["x"] != "alpha" || n.Params["y"] != "beta" || n.Params["z"] != "gamma" {
		t.Errorf("params = %v", n.Params)
	}
}

func TestParsingADependencyResult(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		check func(any) bool
	}{
		{"an object", `{"a":1,"b":"x"}`, func(v any) bool { _, ok := v.(map[string]any); return ok }},
		{"an array", `[1,2,3]`, func(v any) bool { _, ok := v.([]any); return ok }},
		{"prose", "just a plain string", func(v any) bool { return v == "just a plain string" }},
		{"something that starts like JSON and is not", `{not really json`, func(v any) bool { return v == `{not really json` }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseResultForTemplate(c.in); !c.check(got) {
				t.Errorf("got %#v", got)
			}
		})
	}
}

// The body answers the field access, so a body that tolerates the envelope
// wrapper is obeyed and both spellings of the same reference resolve.
//
// The alternative — ask for the whole payload and walk the path here — never
// reaches the body's own handling of the wrapper, so only one spelling works.
func TestBothSpellingsOfAWrappedFieldResolve(t *testing.T) {
	g := NewGraph()
	dep := g.AddNode(&Node{Type: NodeTool, Tag: "search", ToolName: "web_search"})
	g.SetBody(dep, NewToolBody(toolapi.ToolOK("search", "", map[string]any{"url": "https://example.test"})))

	for _, ref := range []string{".url", ".data.url"} {
		n := &Node{ID: "n2", Params: map[string]any{"u": "${node." + dep + ref + "}"}}
		if err := substituteTemplates(n, g, nil); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if n.Params["u"] != "https://example.test" {
			t.Errorf("%s resolved to %v", ref, n.Params["u"])
		}
	}
}

// One dependency, several fields, and the result is parsed once per field
// rather than once: the cache is keyed by (node, field), so every distinct
// field unmarshals the whole result again. Parsing once per dependency and
// walking the paths in memory would do it once.
//
// Recorded rather than fixed. It is a cost, not a defect.
func TestManyFieldsOfOneDependencyReparseIt(t *testing.T) {
	big := `{"a":"1","b":"2","c":"3","d":"4"}`
	g, dep := graphWithDep(t, big, StateResolved)
	n := &Node{ID: "n2", Params: map[string]any{
		"w": "${node." + dep + ".a}", "x": "${node." + dep + ".b}",
		"y": "${node." + dep + ".c}", "z": "${node." + dep + ".d}",
	}}
	if err := substituteTemplates(n, g, nil); err != nil {
		t.Fatalf("substituteTemplates: %v", err)
	}
	for k, want := range map[string]string{"w": "1", "x": "2", "y": "3", "z": "4"} {
		if n.Params[k] != want {
			t.Errorf("%s = %v, want %s", k, n.Params[k], want)
		}
	}
}

// A reference with no path against a tool that returns an envelope gives the
// tool's payload, not the line of text the envelope renders for a human.
//
// SetBody stores the evidence text in Result, so reading Result directly for
// the no-path case handed the next step prose where it wanted the data. Asking
// the body gives the payload, which is what references were written against:
// none of them carries a "data." prefix and every one would break if they had
// to.
//
// Missed by the first pass of this file, because every case here used SetResult
// — raw text — and never an envelope.
func TestABareReferenceToAnEnvelopeGivesThePayload(t *testing.T) {
	g := NewGraph()
	dep := g.AddNode(&Node{Type: NodeTool, ToolName: "process_list"})
	g.SetBody(dep, NewToolBody(toolapi.ToolOK("listing", "2 processes", map[string]any{"count": 2})))

	n := &Node{ID: "n2", Params: map[string]any{"x": "${node." + dep + "}"}}
	if err := substituteTemplates(n, g, nil); err != nil {
		t.Fatalf("substituteTemplates: %v", err)
	}
	got, ok := n.Params["x"].(map[string]any)
	if !ok {
		t.Fatalf("x is %T (%v), want the payload the tool produced", n.Params["x"], n.Params["x"])
	}
	if got["count"] != float64(2) {
		t.Errorf("x[count] = %v, want 2", got["count"])
	}
	if _, isEnvelope := got["status"]; isEnvelope {
		t.Error("x is the envelope rather than the payload — every reference written " +
			"against the payload would need a data. prefix it never had")
	}
}

// A step that declares dependencies and carries a half-written placeholder is
// rejected, not accepted.
//
// The data-flow check used to scan for the text "${node." — which a placeholder
// that was never finished also contains. The step passed, nothing resolved it at
// fire time because the pattern did not match, and the tool was handed the
// literal text. Asking the reference model what is actually there answers the
// question the check meant to ask.
func TestAHalfWrittenPlaceholderDoesNotSatisfyTheDataFlowCheck(t *testing.T) {
	params := map[string]any{"goal": "use ${node. and stop", "mode": "shallow"}
	if err := validateDataFlow("compute", []string{"n1"}, params); err == nil {
		t.Error("a step declaring a dependency was accepted on the strength of text " +
			"that only looks like a reference, so nothing will inject anything and " +
			"the tool receives the placeholder as prose")
	}
}

// And a real reference still satisfies it, so the check did not simply become
// stricter than the thing it guards.
func TestARealReferenceSatisfiesTheDataFlowCheck(t *testing.T) {
	params := map[string]any{"goal": "read ${step.0.content}", "mode": "shallow"}
	if err := validateDataFlow("compute", []string{"n1"}, params); err != nil {
		t.Errorf("a step referencing its dependency was rejected: %v", err)
	}
}
