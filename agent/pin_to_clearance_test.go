package agent

import "testing"

// A pin above the clearance is lowered where the run starts, so everything
// that reads Trigger.Intent before the scheduler sees the lowered pin.
func TestAPinAboveTheClearanceIsLoweredBeforePlanning(t *testing.T) {
	a := &Agent{clearance: &localClearance{value: 0}}
	hundred := 100
	tr := a.pinToClearance(Trigger{MaxIntent: &hundred})
	if tr.MaxIntent == nil || *tr.MaxIntent != 0 || int(tr.Intent()) != 0 {
		t.Errorf("pinned 100 under clearance 0: %v, want 0", tr.Intent())
	}
	if hundred != 100 {
		t.Error("the caller's own pin was changed")
	}
	if tr := a.pinToClearance(Trigger{}); tr.MaxIntent != nil {
		t.Error("no pin became a pin")
	}
	a.clearance.Set(200)
	if tr := a.pinToClearance(Trigger{MaxIntent: &hundred}); *tr.MaxIntent != 100 {
		t.Errorf("a higher clearance raised the pin to %d", *tr.MaxIntent)
	}
}
