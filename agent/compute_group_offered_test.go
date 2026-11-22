package agent

import (
	"slices"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The group compute_mode adds is offered to the planner, not required of it.
//
// Listed under "MUST include at least one tool from each of these groups" it
// had the planner write a compute step for every run preflight marked shallow:
// on 2026-11-22 a buy-or-not question picked only the web group, compute_mode
// shallow added data, and the planner spent 5,228 output tokens on a compute
// step that wrote a prose report.
func TestComputeGroupIsOfferedNotRequired(t *testing.T) {
	picked := []string{toolapi.GroupWeb}
	groups := withComputeGroup(picked, "shallow")
	g := NewGraph()
	g.Preflight = &PreflightResult{Groups: groups, ComputeGroup: toolapi.GroupData}

	if !slices.Contains(pickedGroups(g), toolapi.GroupData) {
		t.Fatalf("the compute group's tools must still be offered, groups %v", pickedGroups(g))
	}
	if req := requiredGroups(g); !slices.Equal(req, []string{toolapi.GroupWeb}) {
		t.Errorf("only the groups preflight picked are required, got %v", req)
	}
}

// A group preflight picked itself stays required, compute group or not.
func TestAPickedDataGroupStaysRequired(t *testing.T) {
	g := NewGraph()
	g.Preflight = &PreflightResult{Groups: withComputeGroup([]string{toolapi.GroupWeb, toolapi.GroupData}, "shallow")}
	if req := requiredGroups(g); !slices.Equal(req, []string{toolapi.GroupWeb, toolapi.GroupData}) {
		t.Errorf("a picked data group must stay required, got %v", req)
	}
}
