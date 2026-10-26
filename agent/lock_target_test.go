package agent

import (
	"context"
	"testing"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A step that runs here runs on this node, so the run's ceiling for this node
// locks its commands, as remoteLevel does for a remote step. It used to lock at
// the intent, clearance and tool cap alone: a run allowed only to read this
// node ran a bash the planner rated a read with nothing locked.
func TestALocalStepIsLockedAtTheRunsCeilingForThisNode(t *testing.T) {
	_, gate, _ := newTestStack(t)
	a := &Agent{gate: gate}
	a.cfg.NodeID = "self"

	cases := []struct {
		name     string
		scope    *ResolvedScope
		scopeCap int
		want     int
	}{
		{"no scope", nil, -1, 200},
		{"no target list", &ResolvedScope{}, -1, 200},
		{"this node read-only", &ResolvedScope{Targets: map[string]int{"self": 0}}, -1, 0},
		{"this node at 100", &ResolvedScope{Targets: map[string]int{"self": 100}}, -1, 100},
		{"tool cap lower than the node's", &ResolvedScope{Targets: map[string]int{"self": 200}}, 50, 50},
		{"this node not listed", &ResolvedScope{Targets: map[string]int{"other": 200}}, -1, 0},
	}
	for _, c := range cases {
		ctx := a.lockContext(context.Background(), gates.Intent(200), c.scope, c.scopeCap)
		got, ok := toolapi.LevelOf(ctx)
		if !ok || got != c.want {
			t.Errorf("%s: level = %d (set %v), want %d", c.name, got, ok, c.want)
		}
	}
}
