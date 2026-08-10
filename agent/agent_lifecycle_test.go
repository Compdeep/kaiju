package agent

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// An agent can be stopped without ending the context it was given.
//
// An application that replaces its agent while the process keeps running hands
// the replacement the same process-lifetime context. Nothing about assigning
// over the old reference ends the kernel, the scheduler workers or the event
// loop it started, so without a way to stop one directly, every rebuild leaves
// a whole agent behind.
//
// Measured by counting goroutines, which is the thing itself rather than a
// proxy for it.

// goroutinesWhenSettled reports the goroutine count once it has stopped moving.
func goroutinesWhenSettled(t *testing.T) int {
	t.Helper()
	last, stable := -1, 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(50 * time.Millisecond)
		n := runtime.NumGoroutine()
		if n == last {
			if stable++; stable == 3 {
				return n
			}
			continue
		}
		last, stable = n, 0
	}
	t.Fatalf("the goroutine count never settled (last %d)", last)
	return 0
}

func startedAgent(t *testing.T, ctx context.Context) *Agent {
	t.Helper()
	d := t.TempDir()
	a, err := New(Config{PathConfig: PathConfig{Workspace: d, MetadataDir: d, DataDir: d}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go a.Start(ctx)
	return a
}

func TestStoppingAnAgentEndsIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	base := goroutinesWhenSettled(t)
	a := startedAgent(t, ctx)
	if running := goroutinesWhenSettled(t); running <= base {
		t.Fatalf("starting an agent added no goroutines: %d then %d", base, running)
	}

	a.Stop()

	if got := goroutinesWhenSettled(t); got > base {
		t.Errorf("%d goroutines after stopping the agent, %d before it started — the "+
			"kernel, the workers and the event loop are still running and the "+
			"context they were given is still open", got, base)
	}
}

// The case the method exists for: one agent replaced by another, on the shared
// context every rebuild is handed. Stopping the old one has to be enough.
func TestReplacingAnAgentLeavesOneRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	base := goroutinesWhenSettled(t)
	old := startedAgent(t, ctx)
	oneAgent := goroutinesWhenSettled(t) - base

	replacement := startedAgent(t, ctx)
	old.Stop()
	t.Cleanup(replacement.Stop)

	if got := goroutinesWhenSettled(t) - base; got > oneAgent {
		t.Errorf("after replacing the agent, %d goroutines are running where one "+
			"agent needs %d", got, oneAgent)
	}
}

// Stop before Start still stops it. Start is called in a goroutine at every
// call site, so a Stop arriving first is a real order rather than a curiosity.
func TestStoppingAnAgentThatHasNotStartedYet(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	base := goroutinesWhenSettled(t)
	d := t.TempDir()
	a, err := New(Config{PathConfig: PathConfig{Workspace: d, MetadataDir: d, DataDir: d}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.Stop()
	go a.Start(ctx)

	if got := goroutinesWhenSettled(t); got > base {
		t.Errorf("%d goroutines, %d before — an agent stopped before it started ran "+
			"anyway", got, base)
	}
}

// Stopping twice is not a panic. A caller that stops an agent and then hits a
// shutdown path should not have to remember which came first.
func TestStoppingAnAgentTwiceIsQuiet(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := startedAgent(t, ctx)
	a.Stop()
	a.Stop()
}
