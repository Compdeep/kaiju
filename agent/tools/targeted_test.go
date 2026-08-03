package tools

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
