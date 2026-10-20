package toolapi

import (
	"context"
	"encoding/json"
	"testing"
)

type plainTool struct{}

func (plainTool) Name() string                { return "plain" }
func (plainTool) Description() string         { return "" }
func (plainTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (plainTool) Impact(map[string]any) int   { return ImpactObserve }
func (plainTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

type aggregateTool struct{ plainTool }

func (aggregateTool) RequiresTarget() bool { return false }

type hostTool struct{ plainTool }

func (hostTool) RequiresTarget() bool { return true }

func TestRequiresTargetDefaultsToTrue(t *testing.T) {
	// A tool that says nothing acts on a specific machine. Defaulting the
	// other way would let a step omit its target and run somewhere unintended
	// without anything noticing.
	if !RequiresTarget(plainTool{}) {
		t.Error("a tool not implementing Targeted must default to requiring a target")
	}
}

func TestRequiresTargetHonoursDeclaration(t *testing.T) {
	if RequiresTarget(aggregateTool{}) {
		t.Error("a tool declaring false must not require a target")
	}
	if !RequiresTarget(hostTool{}) {
		t.Error("a tool declaring true must require a target")
	}
}

type subjectTool struct{ plainTool }

func (subjectTool) ActsOn(p map[string]any) []string { return []string{p["target"].(string)} }

func TestActsOnIsEmptyUnlessDeclared(t *testing.T) {
	if got := ActsOn(plainTool{}, map[string]any{"target": "x"}); got != nil {
		t.Errorf("an undeclared tool acts on %v", got)
	}
	if got := ActsOn(subjectTool{}, map[string]any{"target": "x"}); len(got) != 1 || got[0] != "x" {
		t.Errorf("ActsOn = %v, want [x]", got)
	}
}
