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

	a.recordRun(Trigger{Type: "alert", ID: "a-1"}, time.Now().Add(-time.Second),
		nil, nil, gates.Intent(1), Conclusion{Outcome: "budget_exhausted_before_aggregator", Status: "failed"})

	if len(st.runs) != 1 {
		t.Fatalf("wrote %d runs, want 1", len(st.runs))
	}
	got := st.runs[0]
	if got.Status != "failed" {
		t.Errorf("Status = %q, want failed", got.Status)
	}
	if got.Outcome != "budget_exhausted_before_aggregator" {
		t.Errorf("Outcome = %q, want the reason", got.Outcome)
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
	a.recordRun(Trigger{}, time.Now(), nil, nil, gates.Intent(0), Conclusion{Outcome: "plan_or_schedule_failed", Status: "failed"})
	if len(st.runs) != 1 {
		t.Fatal("an early failure wrote no record")
	}
}

func TestRecordRunIsSilentWithNoStore(t *testing.T) {
	(&Agent{}).recordRun(Trigger{}, time.Now(), nil, nil, gates.Intent(0), Conclusion{Outcome: "x", Status: "failed"})
	var nilAgent *Agent
	nilAgent.recordRun(Trigger{}, time.Now(), nil, nil, gates.Intent(0), Conclusion{Outcome: "x", Status: "failed"})
}

// TestEveryRunExitRecords pins the CALL SITES. A run that returns without
// recording leaves no trace, and nothing else fails to say so.
func TestEveryRunExitRecords(t *testing.T) {
	// Both modes, because a run's mode used to decide whether it was written
	// down at all: the ReAct branch returned above every recordRun call, so an
	// entire execution mode left no trace.
	for _, fn := range []struct{ file, name string }{
		{"scheduler.go", "RunDAGSync"},
		{"loop_react.go", "RunReActSync"},
	} {
		body := funcBody(t, readSource(t, fn.file), fn.name)
		// Every exit that ends the run has to write one. Counting returns rather
		// than a fixed number, so an exit added later without a record fails
		// here — the old form compared against a hardcoded 4 and would have
		// passed with a fifth exit writing nothing.
		exits := strings.Count(body, "return &SyncResult{") + strings.Count(body, "return nil, fmt.Errorf")
		records := strings.Count(body, "a.recordRun(")

		// Three exits are deliberately not recorded and say so where they are:
		// the run answered without planning — a preflight reply, a direct answer,
		// or a refusal to guess. Whether a conversational turn belongs in the
		// same table as an investigation is the APPLICATION's decision, and the
		// engine writing one either way would make it for them.
		//
		// Counted rather than assumed, so a fourth exit added without either a
		// record or the marker fails here and somebody chooses.
		excused := strings.Count(body, "// notrecorded:")

		if records+excused < exits {
			t.Errorf("%s has %d exits, records at %d and excuses %d; a run can end "+
				"leaving nothing to say it happened. Either record it, or mark it "+
				"// notrecorded: <why>", fn.name, exits, records, excused)
		}
		if strings.Contains(body, "InsertRun(Run{") {
			t.Errorf("%s still writes a run inline; it should go through recordRun so every exit is consistent", fn.name)
		}
	}
}

// Admission is asked before the mode is looked at. It used to sit below the
// branch that routes to the ReAct loop, so a request naming that mode was never
// put to the application at all — which mode a caller asked for decided whether
// the application's rule applied to it.
func TestAdmissionIsAskedBeforeTheModeIsChosen(t *testing.T) {
	body := funcBody(t, readSource(t, "scheduler.go"), "RunDAGSync")
	admit := strings.Index(body, "a.admit(trigger)")
	branch := strings.Index(body, `trigger.DAGMode == "react"`)
	if admit < 0 || branch < 0 {
		t.Fatalf("cannot find both the check (%d) and the branch (%d)", admit, branch)
	}
	if admit > branch {
		t.Error("the mode is chosen before the application is asked whether the run " +
			"may start, so one mode skips the rule entirely")
	}
}

// An application that writes its own answer labels it — a severity, a category —
// and those labels are the reason its run record is worth reading. They reach
// the store untouched, or the record says only that something happened.
func TestRecordRunCarriesTheApplicationsLabels(t *testing.T) {
	st := &recordingStore{}
	a := &Agent{eventStore: st, cfg: Config{IdentityConfig: IdentityConfig{NodeID: "n1"}}}

	a.recordRun(Trigger{Type: "event", ID: "corr-2"}, time.Now(), nil, nil, gates.Intent(1),
		Conclusion{Outcome: "the batch finished with two rejects",
			Labels: map[string]string{"grade": "amber", "kind": "batch"}, Status: "completed"})

	if len(st.runs) != 1 {
		t.Fatalf("wrote %d runs, want 1", len(st.runs))
	}
	got := st.runs[0]
	if got.Labels["grade"] != "amber" || got.Labels["kind"] != "batch" {
		t.Errorf("Labels = %v, want the ones the answer carried", got.Labels)
	}
	if got.Outcome != "the batch finished with two rejects" {
		t.Errorf("Outcome = %q", got.Outcome)
	}
}

// NodeID is the machine that ran the work. An application usually wants the
// machine the work was ABOUT — the host an event came from, or the one a command
// was aimed at — and the rule for choosing between them is its own. It can only
// apply that rule if the trigger's routing reaches the record.
//
// An application filtering its run list by host needs these. Without them every
// row would carry the coordinating machine's id, and filtering by any other host
// would return
// nothing.
func TestRunRecordCarriesTheTriggersRouting(t *testing.T) {
	st := &recordingStore{}
	a := &Agent{eventStore: st, cfg: Config{IdentityConfig: IdentityConfig{NodeID: "host-1"}}}

	a.recordRun(Trigger{Type: "alert", ID: "a-3", Source: "host-7", Target: "host-9"},
		time.Now(), nil, nil, gates.Intent(1), Conclusion{Outcome: "v", Status: "completed"})

	if len(st.runs) != 1 {
		t.Fatalf("wrote %d runs, want 1", len(st.runs))
	}
	got := st.runs[0]
	if got.Source != "host-7" {
		t.Errorf("Source = %q, want the host the trigger came from", got.Source)
	}
	if got.Target != "host-9" {
		t.Errorf("Target = %q, want the host it was aimed at", got.Target)
	}
	if got.NodeID != "host-1" {
		t.Errorf("NodeID = %q; it must stay the machine that ran the work", got.NodeID)
	}
}

// A run's identity and its cause are different things. They were the same
// value, so two attempts at one cause produced two records with one identity —
// and an application whose table treats that as a key keeps the first and
// silently discards the second. An application that retries work it could not gather
// evidence for, so the discarded record is the successful attempt and the one
// kept is the failure.
func TestTwoRunsOfOneCauseGetTwoIdentities(t *testing.T) {
	st := &recordingStore{}
	a := &Agent{eventStore: st, cfg: Config{IdentityConfig: IdentityConfig{NodeID: "n1"}}}
	trigger := Trigger{Type: "alert", ID: "a-4"}

	for range 2 {
		// The run id is made where the run begins and handed to the graph, so
		// this stands in for that caller.
		g, _, cleanup := a.setupDAGPipeline(trigger, newRunID(trigger.ID))
		a.recordRun(trigger, time.Now(), g, nil, gates.Intent(1), Conclusion{Outcome: "v", Status: "completed"})
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

	a.recordRun(Trigger{ID: "a-5"}, time.Now(), nil, nil, gates.Intent(0),
		Conclusion{Outcome: "plan_or_schedule_failed", Status: "failed"})

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

	if got := actionRunID(g, "a-6"); got != "a-6-123" {
		t.Errorf("actionRunID = %q, want the run", got)
	}
	// Outside a run the answer is nothing, not the caller's reference.
	// Answering with the reference grouped two runs' actions under one id,
	// which for a state-changing call is the difference between "this ran once"
	// and "this ran twice".
	if got := actionRunID(nil, "a-6"); got != "" {
		t.Errorf("with no run, actionRunID = %q, want nothing", got)
	}
	if got := actionRunID(NewGraph(), "a-6"); got != "" {
		t.Errorf("with an unstamped graph, actionRunID = %q, want nothing", got)
	}
}

// The two counts on a run record measure different things, and one of them was
// named after a third. ReflectionCount is how many points the run stopped to
// reconsider; FollowUpCount is how many of those went on to do more work. It was
// called ReplanCount, and a replan is a different thing in this package — one
// kind of follow-up, not all of them.
func TestTheRunRecordCountsReflectionsAndFollowUps(t *testing.T) {
	g := NewGraph()
	// Two reflections, one of which grafted more work.
	bare := g.AddNode(&Node{Type: NodeReflection, Tag: "reflect-1"})
	withWork := g.AddNode(&Node{Type: NodeReflection, Tag: "reflect-2"})
	child := g.AddNode(&Node{Type: NodeTool, Tag: "grafted", ToolName: "read_file"})
	g.AddChild(withWork, child)
	_ = bare

	st := &recordingStore{}
	a := &Agent{eventStore: st}
	a.recordRun(Trigger{ID: "a-7"}, time.Now(), g, nil, gates.Intent(1),
		Conclusion{Outcome: "v", Status: "completed"})

	got := st.runs[0]
	if got.ReflectionCount != 2 {
		t.Errorf("ReflectionCount = %d, want both reflection points", got.ReflectionCount)
	}
	if got.FollowUpCount != 1 {
		t.Errorf("FollowUpCount = %d, want only the one that grafted work", got.FollowUpCount)
	}
}

// Two runs of one caller reference must be distinguishable everywhere a
// consumer groups by run: the audit line and the DAG event.
//
// The reference repeats by design — an application retrying a piece of work
// hands back the id it was given. Both carried only that reference, so the
// second run's tool calls were indistinguishable from the first's in the audit,
// and its graph was drawn into the first run's view in the browser.
func TestTwoRunsOfOneReferenceAreDistinguishable(t *testing.T) {
	first, second := NewGraph(), NewGraph()
	first.RunID = newRunID("ref-1")
	second.RunID = newRunID("ref-1")

	if first.RunID == second.RunID {
		t.Fatalf("two runs of ref-1 share a run id: %q", first.RunID)
	}
	if actionRunID(first, "ref-1") == actionRunID(second, "ref-1") {
		t.Error("an action from each run groups under the same id")
	}
	for _, g := range []*Graph{first, second} {
		if !strings.HasPrefix(g.RunID, "ref-1-") {
			t.Errorf("RunID = %q, want the reference still readable in it", g.RunID)
		}
	}
}
