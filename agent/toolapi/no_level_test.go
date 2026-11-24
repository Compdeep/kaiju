package toolapi

import (
	"context"
	"testing"

	"github.com/Compdeep/kaiju/sandbox"
)

// A call with no level is held at level 0, as Command holds it, and a nil
// context is such a call rather than a crash.
func TestACallWithNoLevelIsLevelZero(t *testing.T) {
	want, need := sandbox.ReadsEverything(0)
	for name, ctx := range map[string]context.Context{"no level": context.Background(), "nil": nil} {
		got, gotNeed := ReadsEverything(ctx)
		if got != want || gotNeed != need {
			t.Errorf("%s: (%v, %d), want level 0's (%v, %d)", name, got, gotNeed, want, need)
		}
	}
	if ok, _ := ReadsEverything(ForEngine(context.Background())); !ok {
		t.Error("the engine's own read refused")
	}
}
