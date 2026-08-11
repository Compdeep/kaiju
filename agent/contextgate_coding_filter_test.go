package agent

import "testing"

// dropCodingSources strips blueprint/workspace context but leaves neutral
// sources (worklog, node returns) untouched.
func TestDropCodingSources_RemovesCodingKeepsNeutral(t *testing.T) {
	specs := Sources(Worklog(10, "all"), Blueprint(), WorkspaceTree(3), NodeReturns("all"))
	out, dropped := dropCodingSources(specs)

	have := map[string]bool{}
	for _, s := range out {
		have[s.Name] = true
	}
	if !have[SourceWorklog] || !have[SourceNodeReturns] {
		t.Fatalf("neutral sources should survive, got %v", have)
	}
	if have[SourceBlueprint] || have[SourceWorkspaceTree] {
		t.Fatalf("coding sources should be dropped, got %v", have)
	}
	if len(dropped) != 2 {
		t.Errorf("dropped = %v, want blueprint + workspace_tree", dropped)
	}
}

// HasComputeWork is the signal the gate uses to decide whether coding context
// is allowed: preflight ComputeMode set, OR a compute node has been planned.
func TestHasComputeWork(t *testing.T) {
	g := NewGraph()
	if g.HasComputeWork() {
		t.Error("empty graph (no preflight, no compute node) must not be compute work")
	}

	g.Preflight = &PreflightResult{ComputeMode: "deep"}
	if !g.HasComputeWork() {
		t.Error("ComputeMode set → compute work")
	}

	// A planned compute node counts even when preflight didn't flag compute.
	g2 := NewGraph()
	g2.nodes["n1"] = &Node{Type: NodeCompute}
	if !g2.HasComputeWork() {
		t.Error("a planned compute node → compute work")
	}
}
