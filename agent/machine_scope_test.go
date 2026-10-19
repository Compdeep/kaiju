package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// machineTool runs on a machine at a fixed impact; lookupTool runs on none.
type machineTool struct {
	name   string
	impact int
}

func (m *machineTool) Name() string                { return m.name }
func (m *machineTool) Description() string         { return "" }
func (m *machineTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object","properties":{}}`) }
func (m *machineTool) Impact(map[string]any) int   { return m.impact }
func (m *machineTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

type lookupTool struct{ machineTool }

func (l *lookupTool) RequiresTarget() bool { return false }

func machineAgent(t *testing.T, remote bool) *Agent {
	t.Helper()
	reg := toolapi.NewRegistry()
	for _, tool := range []toolapi.Tool{
		&machineTool{name: "read_files", impact: toolapi.ImpactObserve},
		&machineTool{name: "change_files", impact: toolapi.ImpactControl},
		&lookupTool{machineTool{name: "look_up"}},
	} {
		if err := reg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	a := &Agent{registry: reg}
	a.cfg.NodeID = "this-node"
	if remote {
		a.remoteExec = &stubExec{}
	}
	return a
}

func machineStep(tool, target string) *Node {
	return &Node{ID: "n1", Type: NodeTool, ToolName: tool, Target: target}
}

// When targets are in use, a step whose tool runs on a machine must name one.
// It used to run on this node.
func TestAStepThatNamesNoMachineIsRefusedWhenTargetsAreInUse(t *testing.T) {
	a := machineAgent(t, true)
	err := a.checkMachine(machineStep("read_files", ""), nil)
	if err == nil || !strings.Contains(err.Error(), "names none") {
		t.Fatalf("an unnamed machine = %v, want a refusal", err)
	}
	if err := a.checkMachine(machineStep("look_up", ""), nil); err != nil {
		t.Errorf("a tool that runs on no machine was refused: %v", err)
	}
	if err := a.checkMachine(&Node{Type: NodeCompute}, nil); err != nil {
		t.Errorf("a compute node was refused: %v", err)
	}
	// Without remote execution targets are inert, and an unnamed machine is
	// this one, as before.
	if err := machineAgent(t, false).checkMachine(machineStep("read_files", ""), nil); err != nil {
		t.Errorf("with targets inert, an unnamed machine was refused: %v", err)
	}
}

// The scope says which machines, and how far on each. A target is a request;
// the scope is the permission.
func TestTheScopeDecidesTheMachineAndTheImpactThere(t *testing.T) {
	a := machineAgent(t, true)
	scope := &ResolvedScope{Machines: map[string]int{
		"machine-a": toolapi.ImpactControl,
		"machine-b": toolapi.ImpactObserve,
		"this-node": toolapi.ImpactObserve,
	}}
	for _, c := range []struct {
		tool, target string
		allowed      bool
	}{
		{"read_files", "machine-a", true},
		{"change_files", "machine-a", true},
		{"read_files", "machine-b", true},
		{"change_files", "machine-b", false}, // above the ceiling on machine-b
		{"read_files", "machine-c", false},   // not in the scope
		{"read_files", "this-node", true},    // this node is a machine like any other
		{"change_files", "this-node", false},
	} {
		err := a.checkMachine(machineStep(c.tool, c.target), scope)
		if (err == nil) != c.allowed {
			t.Errorf("%s on %s: err = %v, want allowed %v", c.tool, c.target, err, c.allowed)
		}
	}
	// A scope with no machines lets a run reason and look things up, nothing
	// more.
	none := &ResolvedScope{Machines: map[string]int{}}
	if err := a.checkMachine(machineStep("read_files", "machine-a"), none); err == nil {
		t.Error("an empty machine list allowed a machine")
	}
	if err := a.checkMachine(machineStep("look_up", ""), none); err != nil {
		t.Errorf("an empty machine list refused a tool that runs on no machine: %v", err)
	}
	// No list is no limit.
	if err := a.checkMachine(machineStep("change_files", "machine-c"), &ResolvedScope{}); err != nil {
		t.Errorf("a scope without a machine list refused: %v", err)
	}
}

// A tool this node does not know is assumed to run on a machine at the most
// impact, so the scope cannot be slipped past by naming an unknown tool.
func TestAnUnknownToolIsHeldToTheMostImpact(t *testing.T) {
	a := machineAgent(t, true)
	scope := &ResolvedScope{Machines: map[string]int{"machine-b": toolapi.ImpactObserve}}
	if err := a.checkMachine(machineStep("not_registered_here", "machine-b"), scope); err == nil {
		t.Error("an unknown tool ran on a machine held to observe")
	}
}
