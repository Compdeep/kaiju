package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The diagnosis is a read. It was ranked at the cost of the repair behind it,
// which put the one stage that forms and tests a hypothesis out of reach of
// every run at observe rank — and hid it from the reflector, whose tool section
// is rank-filtered and which is the stage meant to steer a re-plan towards it.
func TestDebugToolIsObserve(t *testing.T) {
	d := NewDebugTool(nil)
	if got := d.Impact(nil); got != toolapi.ImpactObserve {
		t.Fatalf("debug Impact = %d, want %d (ImpactObserve) — the write is the microplanner's, and it is gated at dispatch", got, toolapi.ImpactObserve)
	}
}

// A failed step was the only thing the description admitted, so a run that had
// gathered its evidence and still could not account for what it saw had no
// reason to plan one. That is the case Holmes exists for.
func TestDebugToolDescriptionAdmitsAnUnexplainedObservation(t *testing.T) {
	desc := NewDebugTool(nil).Description()
	for _, want := range []string{"ROOT CAUSE", "OBSERVATION", "FAILED"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description does not mention %q:\n%s", want, desc)
		}
	}
	// The old wording forbade exactly the use this is for.
	if strings.Contains(desc, "when nothing actually failed") {
		t.Error("description still rules out a run where nothing failed")
	}
}

// The transient exclusion has to survive the widening: a timeout is retried,
// not diagnosed, and diagnosing one spends a Holmes cycle on the network.
func TestDebugToolStillExcludesTransients(t *testing.T) {
	desc := NewDebugTool(nil).Description()
	for _, want := range []string{"transient", "retried, not diagnosed"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description dropped the transient exclusion (%q):\n%s", want, desc)
		}
	}
}
