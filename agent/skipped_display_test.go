package agent

import (
	"fmt"
	"strings"
	"testing"
)

// A step skipped because an earlier step had nothing to give is not an error of
// its own. The trace showed it as "✗ ERROR dependency injection failed: template
// on n12: …", so a correct skip read as a fault.
func TestASkippedStepIsShownAsSkippedWithItsReason(t *testing.T) {
	g := NewGraph()
	dep := g.AddNode(&Node{Type: NodeTool, Tag: "fetch_helio", ToolName: "web_fetch"})
	n := g.AddNode(&Node{Type: NodeCompute, Tag: "compute_barycenter", ToolName: "compute"})

	blocked := &blockedByDep{Owner: n, DepID: dep, State: StateFailed}
	g.SetBlocked(n, fmt.Errorf("dependency injection failed: %w", blocked))

	info := g.SnapshotNode(n)
	if info.ErrType != "skipped" {
		t.Errorf("err_type = %q, want skipped", info.ErrType)
	}
	if strings.Contains(info.Error, "dependency injection failed") {
		t.Errorf("err = %q, still reads as a fault", info.Error)
	}
	if !strings.Contains(info.Error, "fetch_helio") {
		t.Errorf("err = %q, want it to name the step it was waiting on", info.Error)
	}

	why := &blockedByDep{Owner: n, DepID: dep, State: StateResolved, Why: "search_helio found nothing (no results), so it has no results.0.url to give"}
	g.SetBlocked(n, fmt.Errorf("dependency injection failed: %w", why))
	if got := g.SnapshotNode(n).Error; got != "skipped: "+why.Why {
		t.Errorf("err = %q, want the reason as given", got)
	}
}
