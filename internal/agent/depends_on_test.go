package agent

import (
	"testing"
	"time"
)

// TestPlanStepsToNodes_DependsOnResolves: a normal plan (search → fetch) where the
// fetch is wired to the search via an explicit depends_on index AND a ${step.0…}
// template. The fetch node must end up depending on the search node.
func TestPlanStepsToNodes_DependsOnResolves(t *testing.T) {
	graph := NewGraph()
	budget := NewBudget(100, 100, 100, 100, time.Minute)
	steps := []PlanStep{
		{Tool: "web_search", Params: map[string]any{"query": "x"}, Tag: "s1"},
		{Tool: "web_fetch", Params: map[string]any{"url": "${step.0.results.0.url}"}, Tag: "s2", DependsOn: FlexInts{0}},
	}
	nodes, err := planStepsToNodes(steps, graph, budget, nil)
	if err != nil {
		t.Fatalf("planStepsToNodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(nodes))
	}
	if len(nodes[1].DependsOn) != 1 || nodes[1].DependsOn[0] != nodes[0].ID {
		t.Fatalf("fetch must depend on the search node; got %v (search id %s)", nodes[1].DependsOn, nodes[0].ID)
	}
}

// TestPlanStepsToNodes_NoSelfDependency: regression for the fetch-loop bug. A replan
// often emits depends_on:[0] on its FIRST step — a stale reference to a prior-frame
// search. Resolved plan-locally that is a SELF-dependency: the node waits on itself,
// never fires, is cascaded to StateSkipped, and the reflector re-plans the same
// fetch forever. planStepsToNodes must never wire a node to itself, so a first-step
// literal-URL fetch is immediately runnable.
func TestPlanStepsToNodes_NoSelfDependency(t *testing.T) {
	graph := NewGraph()
	budget := NewBudget(100, 100, 100, 100, time.Minute)
	steps := []PlanStep{
		{Tool: "web_fetch", Params: map[string]any{"url": "https://a.example/1"}, Tag: "s1", DependsOn: FlexInts{0}},
		{Tool: "web_fetch", Params: map[string]any{"url": "https://a.example/2"}, Tag: "s2", DependsOn: FlexInts{0}},
	}
	nodes, err := planStepsToNodes(steps, graph, budget, nil)
	if err != nil {
		t.Fatalf("planStepsToNodes: %v", err)
	}
	for _, d := range nodes[0].DependsOn {
		if d == nodes[0].ID {
			t.Fatalf("step 0 depends on itself (self-cycle → skipped, the fetch-loop bug): %v", nodes[0].DependsOn)
		}
	}
	if len(nodes[0].DependsOn) != 0 {
		t.Fatalf("first literal-URL fetch should have no deps, got %v", nodes[0].DependsOn)
	}
}
