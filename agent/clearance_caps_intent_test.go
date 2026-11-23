package agent

import (
	"testing"

	"github.com/Compdeep/kaiju/agent/gates"
)

// A run never goes past the clearance, whether its intent was pinned or
// inferred. An investigation pinned at 100 on a queen whose operator lowered
// the clearance to 0 runs at 0: remote steps carry the run's intent, and a pin
// used to be trusted outright.
func TestTheClearanceCapsAPinnedIntent(t *testing.T) {
	a := &Agent{clearance: &localClearance{value: 0}}
	if got := a.capToClearance(gates.Intent(100)); got != 0 {
		t.Errorf("pinned 100 under clearance 0: %v, want 0", got)
	}
	a.clearance.Set(100)
	if got := a.capToClearance(gates.Intent(100)); got != 100 {
		t.Errorf("pinned 100 under clearance 100: %v, want 100", got)
	}
	a.clearance.Set(200)
	if got := a.capToClearance(gates.Intent(100)); got != 100 {
		t.Errorf("a higher clearance raised the pin: %v, want 100", got)
	}
	if got := a.capToClearance(gates.IntentAuto); got != gates.IntentAuto {
		t.Errorf("auto became %v", got)
	}
}
