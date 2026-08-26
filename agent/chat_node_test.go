package agent

import (
	"context"
	"encoding/json"
	"testing"
)

// The chat lane is the one path that answered with no node behind it, which is
// why an interjection had nowhere to land. The node has to exist BEFORE the
// answer is written — a steer that arrives mid-answer must attach to something,
// and after the fact it could only reframe prose already produced.
func TestAddInterjectionNode_RecordsTheOperatorMessage(t *testing.T) {
	g := NewGraph()
	id := addInterjectionNode(g, "actually check the other host")

	n := g.SnapshotNode(id)
	if n == nil {
		t.Fatal("the interjection produced no node")
	}
	if n.Type != NodeInterjection.String() {
		t.Errorf("node type = %q, want %q — a trace must read the same whichever lane the steer arrived on",
			n.Type, NodeInterjection.String())
	}
}

// A turn with no steer must cost exactly what it always did. The aggregator is
// the expensive lane and there is nothing to coordinate without a second
// message, so the read has to be non-blocking and answer "no" immediately.
func TestPendingInterjection_NoneWaiting(t *testing.T) {
	if msg, ok := pendingInterjection(context.Background()); ok {
		t.Errorf("a context with no interjection channel reported one: %q", msg)
	}

	ch := make(chan string, 1)
	ctx := withInterject(context.Background(), ch)
	if msg, ok := pendingInterjection(ctx); ok {
		t.Errorf("an empty channel reported a steer: %q", msg)
	}
}

// And when one IS waiting it must be read, not left in the channel — the bug
// this fixes was the scheduler receiving a steer that nothing ever drained.
func TestPendingInterjection_DrainsTheChannel(t *testing.T) {
	ch := make(chan string, 1)
	ch <- "check the other host instead"
	ctx := withInterject(context.Background(), ch)

	msg, ok := pendingInterjection(ctx)
	if !ok || msg != "check the other host instead" {
		t.Fatalf("pendingInterjection = (%q, %v), want the steer", msg, ok)
	}
	// Drained: a second read finds nothing, so one steer cannot be handled twice.
	if again, ok := pendingInterjection(ctx); ok {
		t.Errorf("the steer was left in the channel and read twice: %q", again)
	}
}

// The query is read out of the trigger's data; a malformed or absent payload
// must yield "" rather than panic, since that is what decides whether the lane
// answers at all.
func TestChatQuery(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		data       json.RawMessage
	}{
		{"normal", "what is running on port 22", json.RawMessage(`{"query":"what is running on port 22"}`)},
		{"no query key", "", json.RawMessage(`{"other":"x"}`)},
		{"not an object", "", json.RawMessage(`["x"]`)},
		{"malformed", "", json.RawMessage(`{`)},
		{"nil", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := chatQuery(Trigger{Data: tc.data}); got != tc.want {
				t.Errorf("chatQuery = %q, want %q", got, tc.want)
			}
		})
	}
}

// NodeChat must sit at the END of the iota. A NodeType is written into stored
// traces, so inserting one in the middle changes what every trace already
// written means — the frozen list guards this, and this states why.
func TestNodeChat_IsAppendedNotInserted(t *testing.T) {
	if NodeChat != NodeHolmes+1 {
		t.Errorf("NodeChat = %d, want %d (immediately after NodeHolmes); inserting it earlier renumbers every kind after it",
			NodeChat, NodeHolmes+1)
	}
	if NodeInterjection.String() != "interjection" || NodeHolmes.String() != "holmes" {
		t.Error("adding NodeChat renumbered an existing kind")
	}
}
