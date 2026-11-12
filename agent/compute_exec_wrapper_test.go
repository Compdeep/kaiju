package agent

import (
	"testing"
	"time"
)

// The step that runs a compute step's script writes nothing of its own. It
// teed the output to /tmp/kaiju_<node>.out, which a step below level 200 may
// not create, so the script never ran and every shallow compute failed once.
// bash already keeps the whole output at output_path.
func TestComputeExecRunsTheScriptWithoutATempFile(t *testing.T) {
	g := NewGraph()
	comp := &Node{Type: NodeCompute, Tag: "calc"}
	id := g.AddNode(comp)
	a := &Agent{}
	nodes := a.graftComputeExecution(g, comp, id, "python3 project/x/calc.py", NewBudget(10, 10, 10, 10, time.Minute))
	if len(nodes) != 1 {
		t.Fatalf("grafted %d nodes", len(nodes))
	}
	cmd, _ := nodes[0].Params["command"].(string)
	if cmd != "( python3 project/x/calc.py ) 2>&1" {
		t.Errorf("command = %q", cmd)
	}
}
