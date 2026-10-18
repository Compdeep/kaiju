package agent

import (
	"context"
	"testing"
)

// A ranked run waits behind ordinary background work, a chat still goes first,
// and runs of one rank keep their order.
func TestQueueRank_RankedRunsWaitBehindOrdinaryOnes(t *testing.T) {
	noop := func(context.Context, Trigger) (*SyncResult, error) { return nil, nil }
	s := newScheduler(noop, 3, 100) // workers not started, so jobs stay queued
	a := &Agent{queueRank: func(t Trigger) int {
		if t.Source == "busy-source" {
			return 1
		}
		return 0
	}}
	k := &Kernel{agent: a, scheduler: s}

	for _, tr := range []Trigger{
		{Type: "event", ID: "busy-1", Source: "busy-source"},
		{Type: "event", ID: "busy-2", Source: "busy-source"},
		{Type: "event", ID: "other-1", Source: "quiet-source"},
		{Type: "chat_query", ID: "chat"},
		{Type: "event", ID: "other-2", Source: "quiet-source"},
	} {
		prio, _ := k.schedulePolicy(tr)
		if !s.enqueue(context.Background(), tr, prio, "", nil) {
			t.Fatalf("%s was not queued", tr.ID)
		}
	}

	var order []string
	s.mu.Lock()
	for len(s.pq) > 0 {
		order = append(order, s.pickLocked().trigger.ID)
	}
	s.mu.Unlock()
	want := []string{"chat", "other-1", "other-2", "busy-1", "busy-2"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("order %v, want %v", order, want)
		}
	}
}

// A ranked run is background work all the same: it counts against the
// background cap, so the worker kept for a chat stays free.
func TestQueueRank_ARankedRunIsStillBackground(t *testing.T) {
	if !(PriorityBackground + 5).background() || PriorityChat.background() {
		t.Error("a ranked run is not counted as background, or a chat is")
	}
}

// The rank is held to its bounds, and a panic in the application's code
// queues the run as ordinary.
func TestQueueRank_BoundedAndGuarded(t *testing.T) {
	for give, want := range map[int]int{-5: 0, 3: 3, 1 << 20: maxQueueRank} {
		a := &Agent{queueRank: func(Trigger) int { return give }}
		if got := a.rankInQueue(Trigger{}); got != want {
			t.Errorf("rank %d read as %d, want %d", give, got, want)
		}
	}
	a := &Agent{queueRank: func(Trigger) int { panic("boom") }}
	if got := a.rankInQueue(Trigger{}); got != 0 {
		t.Errorf("a panicking rank read as %d", got)
	}
	var none *Agent
	if got := none.rankInQueue(Trigger{}); got != 0 {
		t.Errorf("no agent ranked %d", got)
	}
}
