package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

type targetTool struct {
	name     string
	requires bool
}

func (t targetTool) Name() string                { return t.name }
func (t targetTool) Description() string         { return t.name }
func (t targetTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (t targetTool) Impact(map[string]any) int   { return toolapi.ImpactObserve }
func (t targetTool) RequiresTarget() bool        { return t.requires }
func (t targetTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

func targetRegistry(t *testing.T) *toolapi.Registry {
	t.Helper()
	r := toolapi.NewRegistry()
	if err := r.Register(targetTool{"inspect_host", true}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(targetTool{"query_store", false}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestApplyRunTargetFillsAndStrips(t *testing.T) {
	reg := targetRegistry(t)
	steps := []PlanStep{
		{Tool: "inspect_host"},                     // needs one, has none -> inherits
		{Tool: "inspect_host", Target: "explicit"}, // already set -> untouched
		{Tool: "query_store", Target: "wrong"},     // takes none -> cleared
		{Tool: "query_store"},                      // takes none, has none -> unchanged
	}
	applyRunTarget(steps, "run-target", reg)

	if steps[0].Target != "run-target" {
		t.Errorf("step 0 should inherit the run target, got %q", steps[0].Target)
	}
	if steps[1].Target != "explicit" {
		t.Errorf("step 1 had its own target overwritten: %q", steps[1].Target)
	}
	if steps[2].Target != "" {
		t.Errorf("step 2 takes no target but kept %q", steps[2].Target)
	}
	if steps[3].Target != "" {
		t.Errorf("step 3 gained a target it should not have: %q", steps[3].Target)
	}
}

// A step needing a target when the run has none must be LEFT unset, not run
// here — a tool that must name a machine has no sensible default, and running
// it locally yields a plausible answer about the wrong host.
func TestApplyRunTargetLeavesUnsetWhenRunHasNoTarget(t *testing.T) {
	reg := targetRegistry(t)
	steps := []PlanStep{{Tool: "inspect_host"}}
	applyRunTarget(steps, "", reg)
	if steps[0].Target != "" {
		t.Errorf("expected the target left unset, got %q", steps[0].Target)
	}
}

func TestApplyRunTargetIgnoresUnknownToolsAndNilRegistry(t *testing.T) {
	steps := []PlanStep{{Tool: "not_registered", Target: "keep"}}
	applyRunTarget(steps, "run", targetRegistry(t))
	if steps[0].Target != "keep" {
		t.Errorf("an unknown tool's target was altered: %q", steps[0].Target)
	}
	applyRunTarget(steps, "run", nil) // must not panic
}
