package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// scopedTool runs on a target at a fixed impact; lookupTool runs on none.
type scopedTool struct {
	name   string
	impact int
}

func (m *scopedTool) Name() string        { return m.name }
func (m *scopedTool) Description() string { return "" }
func (m *scopedTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (m *scopedTool) Impact(map[string]any) int { return m.impact }
func (m *scopedTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

type lookupTool struct{ scopedTool }

func (l *lookupTool) RequiresTarget() bool { return false }

// removeTool runs on no target and acts on the one its "target" argument names.
type removeTool struct{ lookupTool }

func (r *removeTool) ActsOn(p map[string]any) []string {
	if m, _ := p["target"].(string); m != "" {
		return []string{m}
	}
	return nil
}

func targetAgent(t *testing.T, remote bool) *Agent {
	t.Helper()
	reg := toolapi.NewRegistry()
	for _, tool := range []toolapi.Tool{
		&scopedTool{name: "read_files", impact: toolapi.ImpactObserve},
		&scopedTool{name: "change_files", impact: toolapi.ImpactControl},
		&lookupTool{scopedTool{name: "look_up"}},
		&removeTool{lookupTool{scopedTool{name: "remove_target", impact: toolapi.ImpactAffect}}},
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

func targetStep(tool, target string) *Node {
	return &Node{ID: "n1", Type: NodeTool, ToolName: tool, Target: target}
}

// When targets are in use, a step whose tool runs on a target must name one.
// It used to run on this node.
func TestAStepThatNamesNoTargetIsRefusedWhenTargetsAreInUse(t *testing.T) {
	a := targetAgent(t, true)
	err := a.checkTarget(targetStep("read_files", ""), nil)
	if err == nil || !strings.Contains(err.Error(), "names none") {
		t.Fatalf("an unnamed target = %v, want a refusal", err)
	}
	if err := a.checkTarget(targetStep("look_up", ""), nil); err != nil {
		t.Errorf("a tool that runs on no target was refused: %v", err)
	}
	if err := a.checkTarget(&Node{Type: NodeCompute}, nil); err != nil {
		t.Errorf("a compute node was refused: %v", err)
	}
	// Without remote execution targets are inert, and an unnamed target is
	// this one, as before.
	if err := targetAgent(t, false).checkTarget(targetStep("read_files", ""), nil); err != nil {
		t.Errorf("with targets inert, an unnamed target was refused: %v", err)
	}
}

// The scope says which targets, and how far on each. A target is a request;
// the scope is the permission.
func TestTheScopeDecidesTheTargetAndTheImpactThere(t *testing.T) {
	a := targetAgent(t, true)
	scope := &ResolvedScope{Targets: map[string]int{
		"target-a":  toolapi.ImpactControl,
		"target-b":  toolapi.ImpactObserve,
		"this-node": toolapi.ImpactObserve,
	}}
	for _, c := range []struct {
		tool, target string
		allowed      bool
	}{
		{"read_files", "target-a", true},
		{"change_files", "target-a", true},
		{"read_files", "target-b", true},
		{"change_files", "target-b", false}, // above the ceiling on target-b
		{"read_files", "target-c", false},   // not in the scope
		{"read_files", "this-node", true},   // this node is a target like any other
		{"change_files", "this-node", false},
	} {
		err := a.checkTarget(targetStep(c.tool, c.target), scope)
		if (err == nil) != c.allowed {
			t.Errorf("%s on %s: err = %v, want allowed %v", c.tool, c.target, err, c.allowed)
		}
	}
	// A scope with no targets lets a run reason and look things up, nothing
	// more.
	none := &ResolvedScope{Targets: map[string]int{}}
	if err := a.checkTarget(targetStep("read_files", "target-a"), none); err == nil {
		t.Error("an empty target list allowed a target")
	}
	if err := a.checkTarget(targetStep("look_up", ""), none); err != nil {
		t.Errorf("an empty target list refused a tool that runs on no target: %v", err)
	}
	// No list is no limit.
	if err := a.checkTarget(targetStep("change_files", "target-c"), &ResolvedScope{}); err != nil {
		t.Errorf("a scope without a target list refused: %v", err)
	}
}

// A tool this node does not know is assumed to run on a target at the most
// impact, so the scope cannot be slipped past by naming an unknown tool.
func TestAnUnknownToolIsHeldToTheMostImpact(t *testing.T) {
	a := targetAgent(t, true)
	scope := &ResolvedScope{Targets: map[string]int{"target-b": toolapi.ImpactObserve}}
	if err := a.checkTarget(targetStep("not_registered_here", "target-b"), scope); err == nil {
		t.Error("an unknown tool ran on a target held to observe")
	}
}

// A target named in a tool's arguments is held to the same list as a target,
// at the same impact, wherever the tool runs.
func TestATargetAnArgumentNamesIsHeldToTheScope(t *testing.T) {
	a := targetAgent(t, true)
	scope := &ResolvedScope{Targets: map[string]int{
		"target-a": toolapi.ImpactControl,
		"target-b": toolapi.ImpactObserve,
	}}
	remove := func(target string) *Node {
		n := targetStep("remove_target", "")
		n.Params = map[string]any{"target": target}
		return n
	}
	if err := a.checkTarget(remove("target-a"), scope); err != nil {
		t.Errorf("a listed target was refused: %v", err)
	}
	if err := a.checkTarget(remove("target-c"), scope); err == nil || !strings.Contains(err.Error(), "act on target-c") {
		t.Errorf("an unlisted target = %v, want a refusal", err)
	}
	if err := a.checkTarget(remove("target-b"), scope); err == nil {
		t.Error("an affect tool acted on a target held to observe")
	}
	// No list is no limit, and arguments naming no target are not checked.
	if err := a.checkTarget(remove("target-c"), nil); err != nil {
		t.Errorf("a run without a target list refused: %v", err)
	}
	if err := a.checkTarget(remove(""), &ResolvedScope{Targets: map[string]int{}}); err != nil {
		t.Errorf("arguments naming no target were refused: %v", err)
	}
}
