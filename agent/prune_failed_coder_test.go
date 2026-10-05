package agent

import (
	"fmt"
	"testing"
)

// A coder that wrote no file must not leave the node that runs its output ready.
//
// A node is held back from a failed dependency in exactly one way:
// resolveTemplateField raises blockedByDep when a template reads an empty result.
// The architect's execute and service nodes read no template — they carry a literal
// command and depend on every coder for ordering, because a server imports what its
// siblings wrote. So nothing blocked them, and IsTerminal counts StateFailed, which
// left a service node ready after the coder that was supposed to write its entry
// point had failed.

// The shape the architect grafts: one coder, and a service wired to wait for it.
func coderAndItsService(t *testing.T) (*Graph, *Node, *Node) {
	t.Helper()
	g := NewGraph()
	coder := &Node{Type: NodeCompute, ToolName: "compute", Tag: "write_server", State: StatePending}
	cID := g.AddNode(coder)
	service := &Node{
		Type: NodeTool, ToolName: "bash", Tag: "serve_app", State: StatePending,
		Params:    map[string]any{"command": "node server.js"},
		DependsOn: []string{cID},
	}
	g.AddNode(service)
	return g, coder, service
}

// The bug, stated as a fact about the graph: failing the coder does not hold the
// service back, because a failed node is terminal and nothing reads a template.
func TestFailedCoderLeavesItsServiceReadyWithoutAPrune(t *testing.T) {
	g, coder, service := coderAndItsService(t)

	g.SetError(coder.ID, fmt.Errorf("blocked: I was not told what to preserve"))

	if !coder.IsTerminal() {
		t.Fatal("a failed coder is not terminal, so the premise of this test is wrong")
	}
	var ready []string
	for _, n := range g.ReadyNodes() {
		ready = append(ready, n.Tag)
	}
	if len(ready) != 1 || ready[0] != service.Tag {
		t.Fatalf("ready = %v, want just %s — this test exists because it is ready", ready, service.Tag)
	}
}

// And the fix: pruning the failed coder's branch skips it instead, and says so.
func TestPruningAFailedCoderSkipsTheNodeThatWouldRunOnItsOutput(t *testing.T) {
	g, coder, service := coderAndItsService(t)

	g.SetError(coder.ID, fmt.Errorf("blocked: I was not told what to preserve"))
	skipped := g.PruneBranch(coder.ID)

	if len(skipped) != 1 || skipped[0] != "serve_app" {
		t.Fatalf("skipped = %v, want [serve_app] — the caller reports what did not run", skipped)
	}
	if service.State != StateSkipped {
		t.Errorf("the service is %v, want skipped", service.State)
	}
	if n := len(g.ReadyNodes()); n != 0 {
		t.Errorf("%d node(s) still ready after the prune", n)
	}
}

// Pruning follows the whole chain, because the architect wires an execute node to
// ALL coders and a service after that. One failure at the top must not leave
// something two steps down holding a literal command and a satisfied dependency.
func TestPruningReachesTheWholeChain(t *testing.T) {
	g := NewGraph()
	coder := &Node{Type: NodeCompute, Tag: "write_server", State: StatePending}
	cID := g.AddNode(coder)
	build := &Node{Type: NodeTool, Tag: "npm_build", State: StatePending, DependsOn: []string{cID}}
	bID := g.AddNode(build)
	serve := &Node{Type: NodeTool, Tag: "serve_app", State: StatePending, DependsOn: []string{bID}}
	g.AddNode(serve)

	g.SetError(cID, fmt.Errorf("no"))
	skipped := g.PruneBranch(cID)

	if len(skipped) != 2 {
		t.Fatalf("skipped %v, want both npm_build and serve_app", skipped)
	}
	if serve.State != StateSkipped {
		t.Errorf("the node two steps down is %v, want skipped", serve.State)
	}
}

// A sibling that does not depend on the failed coder still runs. The architect
// grafts independent coders in parallel, and one failing is not a reason to
// abandon the others — the reflector replans the one that failed.
func TestPruningLeavesAnIndependentSiblingAlone(t *testing.T) {
	g, coder, _ := coderAndItsService(t)
	sibling := &Node{Type: NodeCompute, Tag: "write_styles", State: StatePending}
	g.AddNode(sibling)

	g.SetError(coder.ID, fmt.Errorf("no"))
	g.PruneBranch(coder.ID)

	if sibling.State != StatePending {
		t.Errorf("an independent sibling is %v, want left pending", sibling.State)
	}
	var ready []string
	for _, n := range g.ReadyNodes() {
		ready = append(ready, n.Tag)
	}
	if len(ready) != 1 || ready[0] != "write_styles" {
		t.Errorf("ready = %v, want just write_styles", ready)
	}
}

// The boundary. Pruning is for the architect's coder children only, because only
// phase 3 wires run nodes to EVERY coder with a literal command. Shallow compute
// keeps the behaviour it had: its exec child and the plan steps ordered after it
// are the planner's sequencing, and a step that would have succeeded on its own
// still gets its turn.
func TestPruningIsForArchitectChildrenOnly(t *testing.T) {
	g := NewGraph()

	// A top-level compute, as edit_file or a plan's compute step produces: no
	// spawner, or one that is not a compute node.
	topLevel := &Node{Type: NodeCompute, Tag: "edit_main_ts", State: StatePending}
	g.AddNode(topLevel)
	if architectChild(g, topLevel) {
		t.Error("a top-level compute reads as an architect child, so the prune would reach shallow")
	}

	// A replan-grafted compute: spawned, but by a reflection.
	refl := &Node{Type: NodeReflection, Tag: "reflect_1", State: StateResolved}
	rID := g.AddNode(refl)
	replanned := &Node{Type: NodeCompute, Tag: "edit_again", State: StatePending, SpawnedBy: rID}
	g.AddNode(replanned)
	if architectChild(g, replanned) {
		t.Error("a replanned compute reads as an architect child")
	}

	// An architect's coder child: spawned by the architect, which is compute.
	arch := &Node{Type: NodeCompute, Tag: "plan_app", State: StateResolved}
	aID := g.AddNode(arch)
	child := &Node{Type: NodeCompute, Tag: "write_server", State: StatePending, SpawnedBy: aID}
	g.AddNode(child)
	if !architectChild(g, child) {
		t.Error("an architect's coder child does not read as one, so the prune never fires")
	}
}
