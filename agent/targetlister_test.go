package agent

import (
	"context"
	"testing"
)

// Which machines a run concerns, for reporting.
//
// Reporting only — nothing is dispatched from this list. The distinction
// matters: a run that says it touches five machines must not thereby run
// anything on five machines.

func TestRunTargetsFallsBackToTheRunsOwnTarget(t *testing.T) {
	a := &Agent{}
	if got := a.runTargets(Trigger{Target: "machine-b"}); len(got) != 1 || got[0] != "machine-b" {
		t.Errorf("runTargets = %v, want just the trigger's own target", got)
	}
	if got := a.runTargets(Trigger{}); got != nil {
		t.Errorf("runTargets = %v, want nil when there is no target", got)
	}
}

func TestRunTargetsUsesTheApplicationsList(t *testing.T) {
	var seen Trigger
	a := &Agent{targetLister: func(t Trigger) []string {
		seen = t
		return []string{"machine-a", "machine-b", "machine-c"}
	}}

	got := a.runTargets(Trigger{Target: "machine-a", AlertID: "a-1"})
	if len(got) != 3 {
		t.Fatalf("runTargets = %v, want the application's three", got)
	}
	if seen.AlertID != "a-1" {
		t.Errorf("the lister was not given the trigger: %+v", seen)
	}
}

// TestRunTargetsIsReportingOnly guards the boundary. remoteFor decides where a
// step actually runs, and it must read the node's own target — never this list.
func TestRunTargetsIsReportingOnly(t *testing.T) {
	a := &Agent{
		cfg:          Config{IdentityConfig: IdentityConfig{NodeID: "self"}},
		targetLister: func(Trigger) []string { return []string{"machine-a", "machine-b"} },
		remoteExec:   &noopExecutor{},
	}
	// A node with no target of its own must not become remotable because the
	// run reports machines.
	if a.remoteFor(&Node{Type: NodeTool}) {
		t.Error("a node with no target was dispatched remotely — the reporting list is routing")
	}
}

type noopExecutor struct{}

func (noopExecutor) Execute(_ context.Context, _ RemoteRequest) (string, error) { return "", nil }
