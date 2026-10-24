package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunCeiling(t *testing.T) {
	if got := runCeiling(10 * time.Minute); got != 40*time.Minute {
		t.Errorf("ceiling for a 10 min wall clock = %s, want 40m", got)
	}
	if got := runCeiling(0); got != 30*time.Minute {
		t.Errorf("ceiling with no wall clock = %s, want 30m", got)
	}
}

// A run that never returns, not even when cancelled, used to hold its worker
// for good. With one worker, the next job proves the worker came back.
func TestRunPastItsCeilingReleasesTheWorker(t *testing.T) {
	s := newScheduler(func(ctx context.Context, tr Trigger) (*SyncResult, error) {
		if tr.ID == "stuck" {
			select {} // ignores ctx entirely
		}
		return &SyncResult{}, nil
	}, 1, 10)
	s.runLimit = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	start := time.Now()
	_, err := s.SubmitSync(ctx, Trigger{ID: "stuck", Type: "event"}, PriorityBackground, "event:stuck")
	if err == nil || !strings.Contains(err.Error(), "hard limit") {
		t.Fatalf("stuck run = %v, want the hard-limit error", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the caller waited %s", d)
	}

	next, cancelNext := context.WithTimeout(ctx, 2*time.Second)
	defer cancelNext()
	if _, err := s.SubmitSync(next, Trigger{ID: "next", Type: "event"}, PriorityBackground, "event:next"); err != nil {
		t.Errorf("the next job did not run on the released worker: %v", err)
	}
}

// A run that finishes inside the ceiling returns as before, and a panic is
// still an ordinary failed job (the guard moved onto the run's goroutine).
func TestRunInsideItsCeilingAndPanics(t *testing.T) {
	s := newScheduler(func(ctx context.Context, tr Trigger) (*SyncResult, error) {
		if tr.ID == "boom" {
			panic("bad state")
		}
		return &SyncResult{}, nil
	}, 1, 10)
	s.runLimit = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	if res, err := s.SubmitSync(ctx, Trigger{ID: "ok"}, PriorityBackground, "event:ok"); err != nil || res == nil {
		t.Errorf("normal run = %v, %v", res, err)
	}
	if _, err := s.SubmitSync(ctx, Trigger{ID: "boom"}, PriorityBackground, "event:boom"); err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Errorf("panicking run = %v, want the panicked error", err)
	}
	if _, err := s.SubmitSync(ctx, Trigger{ID: "after"}, PriorityBackground, "event:after"); err != nil {
		t.Errorf("run after the panic = %v", err)
	}
}
