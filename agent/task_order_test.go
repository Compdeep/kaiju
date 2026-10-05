package agent

import (
	"strings"
	"testing"
)

// depends_on_tasks is an index into the task list, and what arrives in it has to
// survive three kinds of nonsense without stalling the run.
//
// A task naming itself wires a node to its own ID. ReadyNodes needs every
// dependency terminal, so that node never runs and never fails — and since the run
// nodes now wait for every coder to settle, one of those stops the execute, service
// and check nodes from being grafted at all. The whole deep run goes quiet.

func dropReasons(t *testing.T, deps [][]int) ([]taskDepEdge, string) {
	t.Helper()
	live := make([]bool, len(deps))
	for i := range live {
		live[i] = true
	}
	edges, dropped := resolveTaskDeps(deps, live)
	return edges, strings.Join(dropped, " | ")
}

// Ordinary ordering is honoured unchanged.
func TestTaskOrder_AChainIsKept(t *testing.T) {
	edges, dropped := dropReasons(t, [][]int{nil, {0}, {1}})
	if dropped != "" {
		t.Fatalf("a plain chain lost edges: %s", dropped)
	}
	if len(edges) != 2 {
		t.Fatalf("edges = %v, want task 1 after 0 and task 2 after 1", edges)
	}
	if edges[0] != (taskDepEdge{From: 1, To: 0}) || edges[1] != (taskDepEdge{From: 2, To: 1}) {
		t.Errorf("edges = %v, want [{1 0} {2 1}]", edges)
	}
}

// Two tasks may both wait for the same earlier one.
func TestTaskOrder_AFanOutIsKept(t *testing.T) {
	edges, dropped := dropReasons(t, [][]int{nil, {0}, {0}})
	if dropped != "" || len(edges) != 2 {
		t.Fatalf("edges = %v, dropped = %q", edges, dropped)
	}
}

// A task naming itself. This is the stall.
func TestTaskOrder_SelfReferenceIsDroppedAndNamed(t *testing.T) {
	edges, dropped := dropReasons(t, [][]int{nil, {1}})
	if len(edges) != 0 {
		t.Errorf("edges = %v, want none — task 1 would wait for itself", edges)
	}
	if !strings.Contains(dropped, "task 1 depends on itself") {
		t.Errorf("the reason does not say what happened: %q", dropped)
	}
}

// Two tasks naming each other. Both would stall; one edge is enough to break it,
// and the other survives so some of the intended order is kept.
func TestTaskOrder_ACycleKeepsTheFirstEdgeAndBreaksTheSecond(t *testing.T) {
	edges, dropped := dropReasons(t, [][]int{{1}, {0}})
	if len(edges) != 1 || edges[0] != (taskDepEdge{From: 0, To: 1}) {
		t.Errorf("edges = %v, want only {0 1} — the edge that closes the cycle is the one to drop", edges)
	}
	if !strings.Contains(dropped, "already waits for") {
		t.Errorf("the reason does not explain the cycle: %q", dropped)
	}
}

// A longer cycle, which a self-check alone would not catch.
func TestTaskOrder_AThreeWayCycleIsBroken(t *testing.T) {
	edges, dropped := dropReasons(t, [][]int{{2}, {0}, {1}})
	if len(edges) != 2 {
		t.Errorf("edges = %v, want two of the three kept", edges)
	}
	if dropped == "" {
		t.Error("a three-way cycle was wired whole, so all three nodes stall")
	}
	// What survives must be acyclic: nothing may reach back to its own source.
	for _, e := range edges {
		for _, f := range edges {
			if e.From == f.To && e.To == f.From {
				t.Errorf("edges %v and %v still point at each other", e, f)
			}
		}
	}
}

// An index past the end of the plan.
func TestTaskOrder_AnIndexThatIsNotAPlannedTaskIsNamed(t *testing.T) {
	_, dropped := dropReasons(t, [][]int{nil, {9}})
	if !strings.Contains(dropped, "which this plan does not have") {
		t.Errorf("the reason does not say the task is missing: %q", dropped)
	}
	_, negative := dropReasons(t, [][]int{{-1}})
	if !strings.Contains(negative, "does not have") {
		t.Errorf("a negative index was not reported: %q", negative)
	}
}

// A task the budget cut has no node, so nothing can wait for it.
func TestTaskOrder_ATaskWithNoNodeCannotBeWaitedFor(t *testing.T) {
	edges, dropped := resolveTaskDeps([][]int{nil, {0}}, []bool{false, true})
	if len(edges) != 0 {
		t.Errorf("edges = %v, want none — task 0 has no node to wait for", edges)
	}
	if !strings.Contains(strings.Join(dropped, " "), "has no node") {
		t.Errorf("the reason does not say why: %v", dropped)
	}
	// And a task with no node of its own contributes nothing.
	edges, _ = resolveTaskDeps([][]int{nil, {0}}, []bool{true, false})
	if len(edges) != 0 {
		t.Errorf("edges = %v, want none — task 1 has no node to wire", edges)
	}
}
