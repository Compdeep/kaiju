package agent

import (
	"testing"
	"time"
)

// A refused connection and a 429 are both transient, and both belong in the
// blind tier — the difference is when to rerun. The first may already be gone;
// the second is the other end asking to be left alone, and rerunning it at once
// spends the node's one retry proving that.
func TestRetryBackoff_OnlyWaitsWhenToldTo(t *testing.T) {
	now := []string{
		"connection refused",
		"ECONNRESET",
		"npm ERR! network something",
		"exit status 7",
	}
	for _, e := range now {
		if d := retryBackoff(e); d != 0 {
			t.Errorf("%q waits %s — nothing asked it to", e, d)
		}
	}

	wait := []string{
		"page failed: HTTP 429 429 Too Many Requests — https://explorer.solana.com/",
		"rate limit exceeded",
		"HTTP 503 Service Unavailable",
	}
	for _, e := range wait {
		if retryBackoff(e) <= 0 {
			t.Errorf("%q reruns immediately, which is the one thing that cannot work", e)
		}
	}
}

// The classification is unchanged: these are still blind retries, still one
// each. Only the timing moved.
func TestRetryBackoff_TierIsUnchanged(t *testing.T) {
	if got := classifyRetryTier("HTTP 429 Too Many Requests"); got != "blind" {
		t.Errorf("a rate limit is now tier %q, not blind", got)
	}
}

// The hold lives on the node, so the scheduler is not what waits. A step
// serving out a pause must not stop the steps beside it.
func TestHoldUntil_IsPerNode(t *testing.T) {
	g := NewGraph()
	held := g.AddNode(&Node{Type: NodeTool, Tag: "fetch_explorer", ToolName: "web_fetch"})
	other := g.AddNode(&Node{Type: NodeTool, Tag: "fetch_solscan", ToolName: "web_fetch"})

	g.HoldUntil(held, 5*time.Second)

	if d := g.holdFor(held); d <= 0 || d > 5*time.Second {
		t.Errorf("the held node reports %s of wait", d)
	}
	if d := g.holdFor(other); d != 0 {
		t.Errorf("a node nobody held is waiting %s — the whole plan would stall", d)
	}
}

// Zero is a no-op, so the ordinary blind retry keeps rerunning at once.
func TestHoldUntil_ZeroDoesNotHold(t *testing.T) {
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "x", ToolName: "bash"})
	g.HoldUntil(id, 0)
	if d := g.holdFor(id); d != 0 {
		t.Errorf("a zero hold delayed the node by %s", d)
	}
}
