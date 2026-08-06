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
		nil, nil, gates.Intent(1), Conclusion{Verdict: "budget_exhausted_before_aggregator", Status: "failed"})

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
	a.recordRun(Trigger{}, time.Now(), nil, nil, gates.Intent(0), Conclusion{Verdict: "plan_or_schedule_failed", Status: "failed"})
	if len(st.runs) != 1 {
		t.Fatal("an early failure wrote no record")
	}
}

func TestRecordRunIsSilentWithNoStore(t *testing.T) {
	(&Agent{}).recordRun(Trigger{}, time.Now(), nil, nil, gates.Intent(0), Conclusion{Verdict: "x", Status: "failed"})
	var nilAgent *Agent
	nilAgent.recordRun(Trigger{}, time.Now(), nil, nil, gates.Intent(0), Conclusion{Verdict: "x", Status: "failed"})
}

// TestEveryRunExitRecords pins the CALL SITES. A run that returns without
// recording leaves no trace, and nothing else fails to say so.
func TestEveryRunExitRecords(t *testing.T) {
	src := readSource(t, "scheduler.go")
	for _, fn := range []string{"RunDAGSync"} {
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

// An application that writes its own answer labels it — a severity, a category —
// and those labels are the reason its run record is worth reading. They reach
// the store untouched, or the record says only that something happened.
func TestRecordRunCarriesTheApplicationsLabels(t *testing.T) {
	st := &recordingStore{}
	a := &Agent{eventStore: st, cfg: Config{IdentityConfig: IdentityConfig{NodeID: "n1"}}}

	a.recordRun(Trigger{Type: "alert", AlertID: "a-2"}, time.Now(), nil, nil, gates.Intent(1),
		Conclusion{Verdict: "credential theft on web-1", Severity: "high", Category: "intrusion", Status: "completed"})

	if len(st.runs) != 1 {
		t.Fatalf("wrote %d runs, want 1", len(st.runs))
	}
	got := st.runs[0]
	if got.Severity != "high" {
		t.Errorf("Severity = %q, want the label the answer carried", got.Severity)
	}
	if got.Category != "intrusion" {
		t.Errorf("Category = %q, want the label the answer carried", got.Category)
	}
	if got.Verdict != "credential theft on web-1" {
		t.Errorf("Verdict = %q", got.Verdict)
	}
}

// NodeID is the machine that ran the work. An application usually wants the
// machine the work was ABOUT — the host an event came from, or the one a command
// was aimed at — and the rule for choosing between them is its own. It can only
// apply that rule if the trigger's routing reaches the record.
//
// Enbarr filters its investigation list by host. Without these, every row on a
// queen would carry the queen's id and filtering by any pawn would return
// nothing.
func TestRunRecordCarriesTheTriggersRouting(t *testing.T) {
	st := &recordingStore{}
	a := &Agent{eventStore: st, cfg: Config{IdentityConfig: IdentityConfig{NodeID: "queen-1"}}}

	a.recordRun(Trigger{Type: "alert", AlertID: "a-3", Source: "pawn-7", Target: "pawn-9"},
		time.Now(), nil, nil, gates.Intent(1), Conclusion{Verdict: "v", Status: "completed"})

	if len(st.runs) != 1 {
		t.Fatalf("wrote %d runs, want 1", len(st.runs))
	}
	got := st.runs[0]
	if got.Source != "pawn-7" {
		t.Errorf("Source = %q, want the host the trigger came from", got.Source)
	}
	if got.Target != "pawn-9" {
		t.Errorf("Target = %q, want the host it was aimed at", got.Target)
	}
	if got.NodeID != "queen-1" {
		t.Errorf("NodeID = %q; it must stay the machine that ran the work", got.NodeID)
	}
}

// A run's identity and its cause are different things. They were the same
// value, so two attempts at one cause produced two records with one identity —
// and an application whose table treats that as a key keeps the first and
// silently discards the second. Enbarr retries an event it could not gather
// evidence for, so the discarded record is the successful attempt and the one
// kept is the failure.
func TestTwoRunsOfOneCauseGetTwoIdentities(t *testing.T) {
	st := &recordingStore{}
	a := &Agent{eventStore: st, cfg: Config{IdentityConfig: IdentityConfig{NodeID: "n1"}}}
	trigger := Trigger{Type: "alert", AlertID: "a-4"}

	for range 2 {
		g, _, cleanup := a.setupDAGPipeline(trigger)
		a.recordRun(trigger, time.Now(), g, nil, gates.Intent(1), Conclusion{Verdict: "v", Status: "completed"})
		cleanup()
	}

	if len(st.runs) != 2 {
		t.Fatalf("wrote %d runs, want 2", len(st.runs))
	}
	if st.runs[0].ID == st.runs[1].ID {
		t.Errorf("both runs have identity %q; the second overwrites or is dropped", st.runs[0].ID)
	}
	for i, r := range st.runs {
		if r.CorrelationID != "a-4" {
			t.Errorf("run %d lost its cause: CorrelationID = %q", i, r.CorrelationID)
		}
		if !strings.HasPrefix(r.ID, "a-4-") {
			t.Errorf("run %d identity %q does not name its cause; an operator cannot read the row without a join", i, r.ID)
		}
	}
}

// A run recorded with no graph — a failure early enough that none was built —
// still records, falling back to the cause as its identity.
func TestARunWithNoGraphStillRecords(t *testing.T) {
	st := &recordingStore{}
	a := &Agent{eventStore: st}

	a.recordRun(Trigger{AlertID: "a-5"}, time.Now(), nil, nil, gates.Intent(0),
		Conclusion{Verdict: "plan_or_schedule_failed", Status: "failed"})

	if len(st.runs) != 1 || st.runs[0].ID != "a-5" {
		t.Fatalf("runs = %+v, want one identified by its cause", st.runs)
	}
}

// An action belongs to the run that took it, not to every attempt at the cause.
// Without this, a retry's actions are indistinguishable from the first
// attempt's, which for state-changing calls is the difference between "this ran
// once" and "this ran twice".
func TestAnActionNamesTheRunThatTookIt(t *testing.T) {
	g := NewGraph()
	g.RunID = "a-6-123"

	if got := runIDOf(g, "a-6"); got != "a-6-123" {
		t.Errorf("runIDOf = %q, want the run", got)
	}
	if got := runIDOf(nil, "a-6"); got != "a-6" {
		t.Errorf("with no run, runIDOf = %q, want the cause as a fallback", got)
	}
	if got := runIDOf(NewGraph(), "a-6"); got != "a-6" {
		t.Errorf("with an unstamped graph, runIDOf = %q, want the cause", got)
	}
}
