package agent

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// What runs a coder's output is created from the result, so a coder that failed
// produces nothing to stop.
//
// The architect used to graft its execute, service and validation nodes at the
// moment it finished planning, wired to every coder with a literal command. Those
// read no template, so blockedByDep never fired, and IsTerminal counts StateFailed
// — a coder that wrote no file left its service node ready and the service started
// against a file nobody wrote. A prune in the failure path undid that, which made
// compute failures behave differently in deep than in shallow.

// The architect plus its coders, as the up-front graft leaves them: the run nodes
// do not exist yet.
func architectWithCoders(t *testing.T, n int) (*Graph, *Node, []*Node, *Budget) {
	t.Helper()
	g := NewGraph()
	plan := map[string]any{
		"type":       "blueprint",
		"services":   []map[string]any{{"name": "app", "command": "npm start"}},
		"validation": []map[string]any{{"name": "health", "check": "curl -f localhost:3000"}},
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	arch := &Node{Type: NodeCompute, Tag: "plan_app", Result: string(raw)}
	aID := g.AddNode(arch)
	// After AddNode: it sets StatePending on everything it is given.
	arch.State = StateResolved

	var coders []*Node
	for i := 0; i < n; i++ {
		c := &Node{
			Type: NodeCompute, Tag: fmt.Sprintf("write_%d", i), State: StatePending,
			SpawnedBy: aID,
			Params:    map[string]any{"execute": fmt.Sprintf("node build_%d.js", i)},
		}
		cID := g.AddNode(c)
		g.AddChild(aID, cID)
		coders = append(coders, c)
	}
	return g, arch, coders, NewBudget(100, 100, 100, 100, time.Minute)
}

// Nothing is grafted while a coder is still working.
func TestNoRunNodesWhileACoderIsStillCoding(t *testing.T) {
	a := &Agent{}
	g, arch, coders, budget := architectWithCoders(t, 2)

	coders[0].State = StateResolved
	a.graftArchitectRunNodes(g, arch, budget)

	if got := g.NodeCount(); got != 3 {
		t.Errorf("%d nodes, want the architect and its 2 coders — something was grafted early", got)
	}
	if arch.RunGrafted {
		t.Error("the architect is marked as grafted while a coder is still pending")
	}
}

// All resolved: the run and check nodes appear.
func TestRunNodesAppearWhenEveryCoderHasResolved(t *testing.T) {
	a := &Agent{}
	g, arch, coders, budget := architectWithCoders(t, 2)
	for _, c := range coders {
		c.State = StateResolved
	}

	a.graftArchitectRunNodes(g, arch, budget)

	tags := map[string]bool{}
	for _, id := range arch.Children {
		if n := g.Get(id); n != nil {
			tags[n.Tag] = true
		}
	}
	for _, want := range []string{"write_0_exec", "write_1_exec", "app", "verify_health"} {
		if !tags[want] {
			t.Errorf("%s was not grafted; got %v", want, graftedTags(tags))
		}
	}
	if !arch.RunGrafted {
		t.Error("the architect is not marked, so a second coder completion would graft again")
	}
}

// One coder failing means nothing runs. This is the behaviour the prune used to
// produce, now a consequence of when the nodes are built rather than a special
// case in the failure path.
func TestAFailedCoderMeansNoRunNodesAtAll(t *testing.T) {
	a := &Agent{}
	g, arch, coders, budget := architectWithCoders(t, 2)
	coders[0].State = StateResolved
	g.SetError(coders[1].ID, fmt.Errorf("blocked: I was not told what to preserve"))

	a.graftArchitectRunNodes(g, arch, budget)

	if got := g.NodeCount(); got != 3 {
		t.Errorf("%d nodes, want only the architect and its 2 coders — a run node was created "+
			"for a set of files that is incomplete", got)
	}
	if n := len(g.ReadyNodes()); n != 0 {
		t.Errorf("%d node(s) ready after a coder failed", n)
	}
}

// Called on every coder completion, so it must act exactly once.
func TestGraftingTwiceDoesNothingTheSecondTime(t *testing.T) {
	a := &Agent{}
	g, arch, coders, budget := architectWithCoders(t, 1)
	coders[0].State = StateResolved

	a.graftArchitectRunNodes(g, arch, budget)
	first := g.NodeCount()
	a.graftArchitectRunNodes(g, arch, budget)

	if g.NodeCount() != first {
		t.Errorf("node count went %d → %d; the run nodes were grafted twice", first, g.NodeCount())
	}
}

// A check must follow what it checks. The validation node is grafted after the
// run nodes and waits on them, or it tests a server that has not started.
func TestChecksWaitForWhatTheyCheck(t *testing.T) {
	a := &Agent{}
	g, arch, coders, budget := architectWithCoders(t, 1)
	coders[0].State = StateResolved

	a.graftArchitectRunNodes(g, arch, budget)

	var verify, run []string
	for _, id := range arch.Children {
		n := g.Get(id)
		if n == nil {
			continue
		}
		switch {
		case n.Tag == "verify_health":
			verify = n.DependsOn
		case n.Tag == "write_0_exec", n.Tag == "app":
			run = append(run, n.ID)
		}
	}
	if len(verify) == 0 {
		t.Fatal("the check depends on nothing, so it runs before the server starts")
	}
	for _, id := range run {
		found := false
		for _, d := range verify {
			if d == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the check does not wait for run node %s", id)
		}
	}
}

// A step that was waiting for the coders must also wait for what runs them,
// because the coders are resolved by the time those nodes exist.
func TestDownstreamStepsWaitForTheRunNodesToo(t *testing.T) {
	a := &Agent{}
	g, arch, coders, budget := architectWithCoders(t, 1)
	coders[0].State = StateResolved
	// A later plan step, pointed at the coder by the up-front rewrite.
	after := &Node{Type: NodeTool, ToolName: "bash", Tag: "report", State: StatePending,
		DependsOn: []string{coders[0].ID}}
	g.AddNode(after)

	a.graftArchitectRunNodes(g, arch, budget)

	if len(after.DependsOn) < 2 {
		t.Fatalf("report still waits only on %v, so it can run before the server starts", after.DependsOn)
	}
	for _, n := range g.ReadyNodes() {
		if n.Tag == "report" {
			t.Error("report is ready although the run nodes have not completed")
		}
	}
}

func graftedTags(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Which compute nodes the deferred graft applies to. The architect's coder
// children get it; a top-level compute and a replan-grafted compute do not,
// because nothing else is waiting to run their output — the shallow path grafts
// its own exec child from comp.Result.
func TestOnlyArchitectChildrenDriveTheDeferredGraft(t *testing.T) {
	g := NewGraph()

	topLevel := &Node{Type: NodeCompute, Tag: "edit_main_ts"}
	g.AddNode(topLevel)
	if architectChild(g, topLevel) {
		t.Error("a top-level compute reads as an architect child, so shallow would take the deep path")
	}

	refl := &Node{Type: NodeReflection, Tag: "reflect_1"}
	rID := g.AddNode(refl)
	replanned := &Node{Type: NodeCompute, Tag: "edit_again", SpawnedBy: rID}
	g.AddNode(replanned)
	if architectChild(g, replanned) {
		t.Error("a replanned compute reads as an architect child")
	}

	arch := &Node{Type: NodeCompute, Tag: "plan_app"}
	aID := g.AddNode(arch)
	child := &Node{Type: NodeCompute, Tag: "write_server", SpawnedBy: aID}
	g.AddNode(child)
	if !architectChild(g, child) {
		t.Error("an architect's coder child does not read as one, so the run nodes are never grafted")
	}
}
