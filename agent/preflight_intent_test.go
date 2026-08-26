package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// computeStub stands in for the compute tool: the name the reconciler looks
// for, and an impact above read-only.
type computeStub struct{}

func (computeStub) Name() string                { return computeToolName }
func (computeStub) Description() string         { return "runs generated code" }
func (computeStub) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (computeStub) Impact(map[string]any) int   { return toolapi.ImpactAffect }
func (computeStub) RequiresTarget() bool        { return false }
func (computeStub) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

// autoTrigger is a run whose intent the caller left to the engine, which is
// the only kind preflight may adjust.
func autoTrigger() Trigger {
	return Trigger{Type: "chat_query", Data: json.RawMessage(`{"query":"do a thing"}`)}
}

// pinnedTrigger is a run whose caller named the rank it may reach.
func pinnedTrigger(rank int) Trigger {
	return Trigger{Type: "chat_query", Data: json.RawMessage(`{"query":"do a thing"}`), MaxIntent: &rank}
}

func agentForIntent(t *testing.T) *Agent {
	t.Helper()
	reg := toolapi.NewRegistry()
	if err := reg.Register(computeStub{}); err != nil {
		t.Fatal(err)
	}
	return &Agent{registry: reg, intentRegistry: NewIntentRegistry()}
}

// Preflight decides what a run NEEDS and what it is PERMITTED. The two have to
// agree, and nothing checked them.
//
// It answered "this needs compute" and "this is read-only" in one call. The
// planner planned the compute step it was told to, the gate refused it — read-
// only means no side effects — and the run lost the step that would have
// produced the answer, then reported the task impossible.
func TestPreflight_AskingForComputeRaisesAnIntentThatForbidsIt(t *testing.T) {
	a := agentForIntent(t)
	pf := &PreflightResult{Intent: gates.Intent(0), ComputeMode: "shallow"}

	a.reconcileComputeIntent(autoTrigger(), pf)

	needed := a.intentRegistry.ResolveToolIntent(computeToolName, computeStub{}, nil)
	if int(pf.Intent) < needed {
		t.Errorf("intent stayed at %s, which forbids the compute preflight just asked for "+
			"(needs rank(%d))", pf.Intent, needed)
	}
}

// A rank that already permits compute is left where it is. Reconciling is
// making two answers agree, not raising every run that mentions compute.
func TestPreflight_AnIntentThatAlreadyPermitsComputeIsUntouched(t *testing.T) {
	a := agentForIntent(t)
	needed := a.intentRegistry.ResolveToolIntent(computeToolName, computeStub{}, nil)
	pf := &PreflightResult{Intent: gates.Intent(needed + 100), ComputeMode: "deep"}

	a.reconcileComputeIntent(autoTrigger(), pf)

	if int(pf.Intent) != needed+100 {
		t.Errorf("a sufficient rank was changed to %s", pf.Intent)
	}
}

// No compute asked for, no reason to raise anything. A read-only run stays
// read-only.
func TestPreflight_NoComputeLeavesTheRankAlone(t *testing.T) {
	a := agentForIntent(t)
	pf := &PreflightResult{Intent: gates.Intent(0), ComputeMode: ""}

	a.reconcileComputeIntent(autoTrigger(), pf)

	if pf.Intent != gates.Intent(0) {
		t.Errorf("a run needing no compute was raised to %s", pf.Intent)
	}
}

// A caller who pinned a rank has said what the run may do. Preflight is not
// entitled to a second opinion about it — a plan that needs more is refused,
// not quietly permitted.
func TestPreflight_APinnedIntentIsNeverAdjusted(t *testing.T) {
	a := agentForIntent(t)
	pf := &PreflightResult{Intent: gates.Intent(0), ComputeMode: "shallow"}

	a.reconcileComputeIntent(pinnedTrigger(0), pf)

	if pf.Intent != gates.Intent(0) {
		t.Errorf("preflight raised an intent the caller had pinned, to %s", pf.Intent)
	}
}

// A deployment that never registered compute cannot be contradicted about it.
func TestPreflight_NoComputeToolIsNotAContradiction(t *testing.T) {
	a := &Agent{registry: toolapi.NewRegistry(), intentRegistry: NewIntentRegistry()}
	pf := &PreflightResult{Intent: gates.Intent(0), ComputeMode: "shallow"}

	a.reconcileComputeIntent(autoTrigger(), pf)

	if pf.Intent != gates.Intent(0) {
		t.Errorf("intent was raised for a tool this deployment does not have: %s", pf.Intent)
	}
}
