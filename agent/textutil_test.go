package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The budget a tool writes into is the budget this cap enforces.
//
// web_research divides toolapi.EvidenceBudget between the pages it reads so its
// result fits without being cut. If the two numbers drift apart, that tool writes to
// one size and is trimmed at another, and the trim is a head-and-tail cut across its
// sources.
func TestTheEvidenceCapIsTheBudgetToolsAreGiven(t *testing.T) {
	atBudget := strings.Repeat("x", toolapi.EvidenceBudget)
	if got := Text.TruncateEvidence(atBudget); len(got) != toolapi.EvidenceBudget {
		t.Errorf("a result of exactly the budget was cut to %d", len(got))
	}
	// A result over the budget is cut, and says so. The output is a little longer
	// than the budget because the marker is added to it — which is why this checks
	// for the marker rather than comparing lengths, as an earlier version of this
	// test did and got wrong.
	overBudget := strings.Repeat("x", toolapi.EvidenceBudget*2)
	got := Text.TruncateEvidence(overBudget)
	if !strings.Contains(got, "truncated") {
		t.Errorf("a result of twice the budget came back with no mark of being cut: %d chars", len(got))
	}
	if len(got) > toolapi.EvidenceBudget+200 {
		t.Errorf("a cut result is %d chars, which is not the budget plus a marker", len(got))
	}
}
