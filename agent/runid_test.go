package agent

import (
	"context"
	"testing"
)

// A run's identity has to exist before the first model call, not when the graph
// is built.
//
// It used to live only on the Graph, which is created after admission and after
// the calls that route and classify. Those calls write traces, and a trace names
// the file it is written to — so they used the caller's reference instead, and
// two runs of one reference interleaved in one file.
func TestARunKnowsItsIdentityBeforeThereIsAGraph(t *testing.T) {
	ctx := withRunID(context.Background(), "ref-1-123")
	if got := runIDFrom(ctx); got != "ref-1-123" {
		t.Errorf("runIDFrom = %q, want the run stamped on the context", got)
	}
}

// Outside a run there is no run id, and nothing invents one from the caller's
// reference — that is what put two runs in one file.
func TestOutsideARunThereIsNoRunID(t *testing.T) {
	if got := runIDFrom(context.Background()); got != "" {
		t.Errorf("runIDFrom outside a run = %q, want nothing", got)
	}
	if got := runIDFrom(nil); got != "" {
		t.Errorf("runIDFrom(nil) = %q, want nothing", got)
	}
	// An empty id does not stamp the context, so a later read cannot mistake it
	// for a run that was stamped with nothing.
	if got := runIDFrom(withRunID(context.Background(), "")); got != "" {
		t.Errorf("stamping an empty id then reading = %q, want nothing", got)
	}
}

// Two runs of one caller reference write to two files.
func TestTwoRunsOfOneReferenceWriteSeparateTraces(t *testing.T) {
	first := runIDFrom(withRunID(context.Background(), newRunID("ref-1")))
	second := runIDFrom(withRunID(context.Background(), newRunID("ref-1")))

	if first == "" || second == "" {
		t.Fatalf("a stamped run read back empty: %q, %q", first, second)
	}
	if first == second {
		t.Errorf("both runs name the same trace file: %q", first)
	}
}
