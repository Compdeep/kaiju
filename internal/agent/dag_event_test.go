package agent

import (
	"sync"
	"testing"
)

// TestBroadcastDAGEvent_PerGraphSessionTagging is the spine acceptance test:
// concurrent investigations each carry their own *Graph, and every event is
// tagged with its emitting graph's SessionID at broadcast time — never from a
// shared Agent field a concurrent run could clobber. Run under -race.
func TestBroadcastDAGEvent_PerGraphSessionTagging(t *testing.T) {
	a := &Agent{dagSubs: map[int]chan DAGEvent{}}
	ch, unsub := a.SubscribeDAG()
	defer unsub()

	gA := &Graph{SessionID: "sess-A"}
	gB := &Graph{SessionID: "sess-B"}

	const each = 30 // 60 total fits the 64-deep subscriber buffer with no drops
	var wg sync.WaitGroup
	for i := 0; i < each; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); a.broadcastDAGEvent(gA, DAGEvent{Type: "node", NodeID: "x"}) }()
		go func() { defer wg.Done(); a.broadcastDAGEvent(gB, DAGEvent{Type: "node", NodeID: "y"}) }()
	}
	wg.Wait()

	want := map[string]string{"x": "sess-A", "y": "sess-B"}
	got := 0
	for drained := false; !drained; {
		select {
		case evt := <-ch:
			if evt.SessionID != want[evt.NodeID] {
				t.Fatalf("node %q tagged session %q, want %q", evt.NodeID, evt.SessionID, want[evt.NodeID])
			}
			got++
		default:
			drained = true
		}
	}
	if got != 2*each {
		t.Fatalf("received %d correctly-tagged events, want %d", got, 2*each)
	}
}
