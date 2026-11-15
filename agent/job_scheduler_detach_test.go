package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A chat run is submitted with a context that its caller hanging up does not
// cancel (internal/api handleExecute, Enbarr's handleChat). The run must keep
// going when the caller leaves, still take a steering message, and still stop
// on the Stop button, which cancels through the scheduler.
func TestDetachedRunStillSteersAndStops(t *testing.T) {
	steered := make(chan string, 1)
	started := make(chan struct{})
	exec := func(ctx context.Context, tr Trigger) (*SyncResult, error) {
		close(started)
		for {
			select {
			case msg := <-interjectFrom(ctx):
				steered <- msg
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	s := newScheduler(exec, 2, 10)
	schedCtx, stopSched := context.WithCancel(context.Background())
	defer stopSched()
	s.Start(schedCtx)

	caller, hangUp := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.SubmitSync(context.WithoutCancel(caller), Trigger{ID: "chat-1", Type: "chat_query"}, PriorityChat, "session-1")
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never started")
	}

	hangUp()
	select {
	case err := <-done:
		t.Fatalf("the run ended when the caller left: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	if !s.Interject("session-1", "also give it in AU") {
		t.Fatal("Interject found no running query after the caller left")
	}
	select {
	case msg := <-steered:
		if msg != "also give it in AU" {
			t.Fatalf("steer = %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the steering message never reached the run")
	}

	if !s.Cancel("session-1") {
		t.Fatal("Stop found no running query")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stop ended the run with %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not end the run")
	}
}
