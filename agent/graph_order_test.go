package agent

import (
	"testing"
)

// The order steps were added in is the order every reader sees.
//
// Nodes live in a map, and Go randomises map iteration deliberately. Every
// reader that walked it got a different order each run: independent steps
// launched in one order and the trace showed another, and neither was the order
// the planner wrote. Nothing was wrong often enough to notice — it was just
// never the same twice.
//
// Repeated because one pass over a map can agree with insertion order by
// chance. Twenty will not.
func TestGraph_ReadersSeeTheOrderStepsWereAddedIn(t *testing.T) {
	const steps = 12
	want := make([]string, 0, steps)

	g := NewGraph()
	for i := 0; i < steps; i++ {
		id := g.AddNode(&Node{Type: NodeTool, ToolName: "web_fetch", Tag: "fetch"})
		want = append(want, id)
	}

	for pass := 0; pass < 20; pass++ {
		var got []string
		for _, n := range g.PendingNodes() {
			got = append(got, n.ID)
		}
		if !sameOrder(got, want) {
			t.Fatalf("pass %d: PendingNodes gave %v, want %v", pass, got, want)
		}

		got = got[:0]
		for _, info := range g.Snapshot() {
			got = append(got, info.ID)
		}
		if !sameOrder(got, want) {
			t.Fatalf("pass %d: Snapshot gave %v, want %v", pass, got, want)
		}

		got = got[:0]
		for _, n := range g.ReadyNodes() {
			got = append(got, n.ID)
		}
		if !sameOrder(got, want) {
			t.Fatalf("pass %d: ReadyNodes gave %v, want %v", pass, got, want)
		}
	}
}

// A step waits for what it depends on, and the ones that are ready keep their
// order among themselves.
func TestGraph_ReadyKeepsOrderAmongTheStepsThatCanRun(t *testing.T) {
	g := NewGraph()
	a := g.AddNode(&Node{Type: NodeTool, ToolName: "web_fetch", Tag: "first"})
	b := g.AddNode(&Node{Type: NodeTool, ToolName: "web_fetch", Tag: "second"})
	c := g.AddNode(&Node{Type: NodeTool, ToolName: "compute", Tag: "third", DependsOn: []string{a}})
	d := g.AddNode(&Node{Type: NodeTool, ToolName: "web_fetch", Tag: "fourth"})

	var ready []string
	for _, n := range g.ReadyNodes() {
		ready = append(ready, n.ID)
	}
	if !sameOrder(ready, []string{a, b, d}) {
		t.Fatalf("ready is %v, want the three with no dependency, in order: %v", ready, []string{a, b, d})
	}
	if hasID(ready, c) {
		t.Error("a step whose dependency has not finished was ready")
	}
}

// Order survives a failure. Its dependents stop being reachable; everything
// else keeps its place.
func TestGraph_OrderSurvivesAFailure(t *testing.T) {
	g := NewGraph()
	a := g.AddNode(&Node{Type: NodeTool, ToolName: "web_fetch", Tag: "first"})
	b := g.AddNode(&Node{Type: NodeTool, ToolName: "web_fetch", Tag: "second"})
	c := g.AddNode(&Node{Type: NodeTool, ToolName: "web_fetch", Tag: "third"})

	g.SetState(b, StateFailed)

	var ready []string
	for _, n := range g.ReadyNodes() {
		ready = append(ready, n.ID)
	}
	if !sameOrder(ready, []string{a, c}) {
		t.Fatalf("ready is %v, want %v — a failure moved the others", ready, []string{a, c})
	}
}

// A retry re-pends the same node, so it comes back in its own place rather than
// at the end.
func TestGraph_ARetryReturnsToItsOwnPlace(t *testing.T) {
	g := NewGraph()
	a := g.AddNode(&Node{Type: NodeTool, ToolName: "web_fetch", Tag: "first"})
	b := g.AddNode(&Node{Type: NodeTool, ToolName: "web_fetch", Tag: "second"})
	c := g.AddNode(&Node{Type: NodeTool, ToolName: "web_fetch", Tag: "third"})

	g.SetState(a, StateResolved)
	g.SetState(b, StateFailed)
	g.SetState(b, StatePending) // what a blind retry does

	var ready []string
	for _, n := range g.ReadyNodes() {
		ready = append(ready, n.ID)
	}
	if !sameOrder(ready, []string{b, c}) {
		t.Fatalf("ready is %v, want %v — the retry did not return to its place", ready, []string{b, c})
	}
}

func sameOrder(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func hasID(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
