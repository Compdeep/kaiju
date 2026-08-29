package agent

import (
	"context"
	"testing"
	"time"
)

// The throttle is enforced per destination, and the key is what makes that so.
//
// Keyed by tool alone, a plan reading ten different sites would queue behind
// itself for no reason. Keyed by tool and destination, calls to one site wait
// for each other and calls to different sites do not.
func TestThrottleKey_SeparatesDestinations(t *testing.T) {
	st := newToolThrottle()
	const cooldown = 80 * time.Millisecond

	// First call through either key returns at once — nothing has fired yet.
	st.waitThrottle(context.Background(), "web_fetch\x00a.example", cooldown)

	start := time.Now()
	st.waitThrottle(context.Background(), "web_fetch\x00b.example", cooldown)
	if waited := time.Since(start); waited > cooldown/2 {
		t.Errorf("a call to another destination waited %s — one site's limit is slowing down another", waited)
	}

	start = time.Now()
	st.waitThrottle(context.Background(), "web_fetch\x00a.example", cooldown)
	if waited := time.Since(start); waited < cooldown/2 {
		t.Errorf("a second call to the SAME destination waited only %s, so the two arrive together", waited)
	}
}

// A tool that says nothing about destinations keeps the behaviour it had: one
// key, and its calls spaced against each other.
func TestThrottleKey_AnUnaddressedToolThrottlesAsAWhole(t *testing.T) {
	st := newToolThrottle()
	const cooldown = 80 * time.Millisecond

	st.waitThrottle(context.Background(), "some_tool\x00", cooldown)
	start := time.Now()
	st.waitThrottle(context.Background(), "some_tool\x00", cooldown)
	if waited := time.Since(start); waited < cooldown/2 {
		t.Errorf("an unaddressed tool's second call waited %s, so its declared throttle does nothing", waited)
	}
}

// A cancelled run does not sit out the wait. A plan being torn down while a
// dozen calls queue on one host would otherwise take the whole cooldown times
// twelve to notice.
func TestThrottleKey_CancellationEndsTheWait(t *testing.T) {
	st := newToolThrottle()
	const cooldown = 5 * time.Second

	st.waitThrottle(context.Background(), "web_fetch\x00a.example", cooldown)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	st.waitThrottle(ctx, "web_fetch\x00a.example", cooldown)
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("a cancelled call waited %s of a %s cooldown", waited, cooldown)
	}
}
