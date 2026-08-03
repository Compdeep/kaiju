package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// controllableExec returns an executor whose runs block until explicitly
// released, so tests can observe scheduling decisions deterministically.
func controllableExec() (exec func(context.Context, Trigger) (*SyncResult, error), started <-chan string, release func(string)) {
	var mu sync.Mutex
	gates := map[string]chan struct{}{}
	startedCh := make(chan string, 16)
	exec = func(ctx context.Context, tr Trigger) (*SyncResult, error) {
		gate := make(chan struct{})
		mu.Lock()
		gates[tr.AlertID] = gate
		mu.Unlock()
		startedCh <- tr.AlertID
		select {
		case <-gate:
			return &SyncResult{Verdict: tr.AlertID}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	release = func(id string) {
		mu.Lock()
		gate := gates[id]
		mu.Unlock()
		if gate != nil {
			close(gate)
		}
	}
	return exec, startedCh, release
}

// With 2+ workers, one is reserved for interactive chat: a flood of background
// jobs must never occupy the worker an incoming chat query needs.
func TestScheduler_ReservesLaneForChat(t *testing.T) {
	exec, started, release := controllableExec()
	s := newScheduler(exec, 2, 100) // 2 workers → maxBackground = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	// First background job runs on one worker.
	s.Submit(Trigger{AlertID: "bgA"}, PriorityBackground, "alert:bgA")
	if got := <-started; got != "bgA" {
		t.Fatalf("expected bgA to start, got %s", got)
	}

	// Second background job is held back even though a worker is idle — that
	// worker is reserved for chat.
	s.Submit(Trigger{AlertID: "bgB"}, PriorityBackground, "alert:bgB")
	select {
	case got := <-started:
		t.Fatalf("bgB started, but the second worker should be reserved for chat: %s", got)
	case <-time.After(40 * time.Millisecond):
	}

	// A chat query takes the reserved worker immediately.
	go s.SubmitSync(ctx, Trigger{AlertID: "chatC"}, PriorityChat, "t1")
	if got := <-started; got != "chatC" {
		t.Fatalf("expected chatC to start on the reserved worker, got %s", got)
	}

	// When the first background job finishes, the held-back bgB runs.
	release("bgA")
	if got := <-started; got != "bgB" {
		t.Fatalf("expected bgB to run after bgA freed a background slot, got %s", got)
	}
	release("bgB")
	release("chatC")
}

// At the queue cap, background investigations are rejected but an interactive
// chat must still enqueue — it outranks background work and has a reserved
// worker, so "queue full" must never block the operator's chat.
func TestScheduler_ChatBypassesQueueCap(t *testing.T) {
	noop := func(context.Context, Trigger) (*SyncResult, error) { return nil, nil }
	s := newScheduler(noop, 3, 5) // maxDepth = 5, workers not started so jobs stay queued

	for i := 0; i < 5; i++ {
		if !s.enqueue(context.Background(), Trigger{Type: "background"}, PriorityBackground, fmt.Sprintf("bg-%d", i), nil) {
			t.Fatalf("background %d should enqueue below cap", i)
		}
	}
	// At cap: another background is rejected...
	if s.enqueue(context.Background(), Trigger{Type: "background"}, PriorityBackground, "bg-extra", nil) {
		t.Error("background at cap should be rejected")
	}
	// ...but a chat flows through.
	if !s.enqueue(context.Background(), Trigger{Type: "chat_query"}, PriorityChat, "chat-1", nil) {
		t.Error("chat must bypass the queue cap")
	}
}
