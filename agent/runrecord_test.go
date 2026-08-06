package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/gates"
)

// A run that fails is a fact about the system, not an absence of one. Someone
// asking why nothing happened overnight needs the row that says the budget ran
// out — a failed node broadcast to a live view is gone by morning.

type recordingStore struct{ runs []Run }

func (r *recordingStore) InsertRun(run Run) error   { r.runs = append(r.runs, run); return nil }
func (r *recordingStore) InsertAction(Action) error { return nil }

func TestRecordRunWritesTheOutcome(t *testing.T) {
	st := &recordingStore{}
	a := &Agent{eventStore: st, cfg: Config{IdentityConfig: IdentityConfig{NodeID: "n1"}}}

	a.recordRun(Trigger{Type: "alert", AlertID: "a-1"}, time.Now().Add(-time.Second),
		nil, nil, gates.Intent(1), "budget_exhausted_before_aggregator", "failed")

	if len(st.runs) != 1 {
		t.Fatalf("wrote %d runs, want 1", len(st.runs))
	}
	got := st.runs[0]
	if got.Status != "failed" {
		t.Errorf("Status = %q, want failed", got.Status)
	}
	if got.Verdict != "budget_exhausted_before_aggregator" {
		t.Errorf("Verdict = %q, want the reason", got.Verdict)
	}
	if got.DurationMs <= 0 {
		t.Errorf("DurationMs = %d, want the elapsed time", got.DurationMs)
	}
}

// A very early failure has no graph or budget yet. Recording must still work,
// or the exits that most need a record are the ones that cannot write one.
func TestRecordRunToleratesNoGraphOrBudget(t *testing.T) {
	st := &recordingStore{}
	a := &Agent{eventStore: st}
	a.recordRun(Trigger{}, time.Now(), nil, nil, gates.Intent(0), "plan_or_schedule_failed", "failed")
	if len(st.runs) != 1 {
		t.Fatal("an early failure wrote no record")
	}
}

func TestRecordRunIsSilentWithNoStore(t *testing.T) {
	(&Agent{}).recordRun(Trigger{}, time.Now(), nil, nil, gates.Intent(0), "x", "failed")
	var nilAgent *Agent
	nilAgent.recordRun(Trigger{}, time.Now(), nil, nil, gates.Intent(0), "x", "failed")
}

// TestEveryRunExitRecords pins the CALL SITES. A run that returns without
// recording leaves no trace, and nothing else fails to say so.
func TestEveryRunExitRecords(t *testing.T) {
	src := readSource(t, "scheduler.go")
	for _, fn := range []string{"runDAG", "RunDAGSync"} {
		body := funcBody(t, src, fn)
		// count exits that end the run against recordRun calls
		if strings.Count(body, "a.recordRun(") < 4 {
			t.Errorf("%s records at only %d exits; the failure paths write nothing",
				fn, strings.Count(body, "a.recordRun("))
		}
		if strings.Contains(body, "InsertRun(Run{") {
			t.Errorf("%s still writes a run inline; it should go through recordRun so every exit is consistent", fn)
		}
	}
}
