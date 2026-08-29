package agent

import (
	"slices"
	"strings"
	"testing"
)

// compute resolves, an exec child is grafted to run the script it wrote, and only
// when that child finishes is its stdout merged onto the parent as `output`. A
// step wired to ${node.<compute>.output} depends on the compute node, so it and
// the child became ready in the same instant and the step could be dispatched
// first — failing with `field "output" absent in dep n8`, which is what ended a
// run. The graph now orders it.
func TestWaitAlsoOn_OrdersOnlyTheNodesThatReadTheField(t *testing.T) {
	g := NewGraph()
	compute := g.AddNode(&Node{Type: NodeCompute, Tag: "make_script"})
	g.SetState(compute, StateResolved)

	reader := g.AddNode(&Node{Type: NodeTool, ToolName: "bash", Tag: "reads_output",
		DependsOn: []string{compute},
		Params:    map[string]any{"command": "echo ${node." + compute + ".output}"}})
	other := g.AddNode(&Node{Type: NodeTool, ToolName: "bash", Tag: "reads_something_else",
		DependsOn: []string{compute},
		Params:    map[string]any{"command": "cat ${node." + compute + ".code_path}"}})
	unrelated := g.AddNode(&Node{Type: NodeTool, ToolName: "bash", Tag: "unrelated",
		Params: map[string]any{"command": "true"}})
	child := g.AddNode(&Node{Type: NodeTool, ToolName: "bash", Tag: "exec_make_script",
		DependsOn: []string{compute}, SpawnedBy: compute})

	waited := g.WaitAlsoOn(compute, child, "output")

	if len(waited) != 1 || waited[0] != reader {
		t.Fatalf("only the node reading .output should wait, got %v", waited)
	}
	if !slices.Contains(g.Get(reader).DependsOn, child) {
		t.Fatalf("the reader must now wait for the child, deps are %v", g.Get(reader).DependsOn)
	}
	// A node reading a field the compute already carries must keep its own dependencies,
	// or every dependent of every compute would be serialised behind its script.
	if slices.Contains(g.Get(other).DependsOn, child) {
		t.Fatal("a node reading a field that is already present must not be delayed")
	}
	if len(g.Get(unrelated).DependsOn) != 0 {
		t.Fatalf("a node depending on nothing must be left alone, got %v", g.Get(unrelated).DependsOn)
	}
	if slices.Contains(g.Get(child).DependsOn, child) {
		t.Fatal("the child must never be made to wait for itself")
	}
}

// Only a node still waiting can be reordered. One already running or finished
// has read what it was going to read.
func TestWaitAlsoOn_LeavesNodesThatAlreadyStarted(t *testing.T) {
	g := NewGraph()
	compute := g.AddNode(&Node{Type: NodeCompute, Tag: "make_script"})
	running := g.AddNode(&Node{Type: NodeTool, ToolName: "bash", DependsOn: []string{compute},
		Params: map[string]any{"command": "echo ${node." + compute + ".output}"}})
	g.SetState(running, StateRunning)
	child := g.AddNode(&Node{Type: NodeTool, ToolName: "bash", DependsOn: []string{compute}, SpawnedBy: compute})

	if waited := g.WaitAlsoOn(compute, child, "output"); len(waited) != 0 {
		t.Fatalf("a node already running cannot be reordered, got %v", waited)
	}
}

// Called twice — a second graft, or a retry — must not stack duplicate dependencies.
func TestWaitAlsoOn_DoesNotRepeatItself(t *testing.T) {
	g := NewGraph()
	compute := g.AddNode(&Node{Type: NodeCompute, Tag: "make_script"})
	reader := g.AddNode(&Node{Type: NodeTool, ToolName: "bash", DependsOn: []string{compute},
		Params: map[string]any{"command": "echo ${node." + compute + ".output}"}})
	child := g.AddNode(&Node{Type: NodeTool, ToolName: "bash", DependsOn: []string{compute}, SpawnedBy: compute})

	g.WaitAlsoOn(compute, child, "output")
	if second := g.WaitAlsoOn(compute, child, "output"); len(second) != 0 {
		t.Fatalf("the dependency already exists, so nothing should be added again: %v", second)
	}
	deps := g.Get(reader).DependsOn
	if strings.Count(strings.Join(deps, " "), child) != 1 {
		t.Fatalf("the child must appear once, deps are %v", deps)
	}
}
