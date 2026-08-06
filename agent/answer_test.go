package agent

import (
	"context"
	"errors"
	"os"
	"regexp"
	"testing"
)

// An application supplying Answer is asked to write each run's answer.
func TestTheSuppliedAnswerIsUsed(t *testing.T) {
	a := &Agent{answer: func(_ context.Context, _ AnswerRequest) (*AnswerResult, error) {
		return &AnswerResult{Text: "credential theft on web-1", Severity: "high", Category: "intrusion"}, nil
	}}

	res, ok, err := a.writeAnswer(context.Background(), AnswerRequest{})

	if err != nil || !ok {
		t.Fatalf("writeAnswer = %v, %v, %v", res, ok, err)
	}
	if res.Text != "credential theft on web-1" || res.Severity != "high" || res.Category != "intrusion" {
		t.Errorf("the answer came back altered: %+v", res)
	}
}

// Declining a single run is how an application answers some kinds of run itself
// and leaves the rest to the built-in aggregator. It says so by returning
// nothing, so this package needs no rule of its own about which runs are whose.
func TestReturningNothingLeavesTheRunToTheBuiltInAggregator(t *testing.T) {
	a := &Agent{answer: func(_ context.Context, req AnswerRequest) (*AnswerResult, error) {
		if req.Trigger.Type != "alert" {
			return nil, nil
		}
		return &AnswerResult{Text: "verdict"}, nil
	}}

	if _, ok, _ := a.writeAnswer(context.Background(), AnswerRequest{Trigger: Trigger{Type: "chat_query"}}); ok {
		t.Error("a declined run was reported as answered")
	}
	if _, ok, _ := a.writeAnswer(context.Background(), AnswerRequest{Trigger: Trigger{Type: "alert"}}); !ok {
		t.Error("an accepted run was reported as declined")
	}
}

// No capability at all is the ordinary case, and must behave exactly as it did
// before the capability existed.
func TestNoAnswerCapabilityLeavesEveryRunToTheBuiltInAggregator(t *testing.T) {
	if _, ok, err := (&Agent{}).writeAnswer(context.Background(), AnswerRequest{}); ok || err != nil {
		t.Errorf("writeAnswer = %v, %v; want declined and no error", ok, err)
	}
	var nilAgent *Agent
	if _, ok, err := nilAgent.writeAnswer(context.Background(), AnswerRequest{}); ok || err != nil {
		t.Errorf("a nil agent should decline quietly, got %v, %v", ok, err)
	}
}

// Failing to write an answer that was meant to be written fails the run. The
// alternative — falling through to the built-in aggregator — would answer in the
// wrong shape and record it as a success, which is worse than no answer.
func TestAFailedAnswerFailsTheRun(t *testing.T) {
	a := &Agent{answer: func(_ context.Context, _ AnswerRequest) (*AnswerResult, error) {
		return nil, errors.New("model refused the schema")
	}}

	res, ok, err := a.writeAnswer(context.Background(), AnswerRequest{})

	if err == nil {
		t.Fatal("the failure was swallowed")
	}
	if ok || res != nil {
		t.Errorf("a failed answer must not be reported as one: %v, %v", res, ok)
	}
}

// An answer with no readable text is an application bug, not a reason to throw
// away a finished run: the evidence is gathered and the structured result may
// still be usable.
func TestAnAnswerWithNoTextStillCounts(t *testing.T) {
	a := &Agent{answer: func(_ context.Context, _ AnswerRequest) (*AnswerResult, error) {
		return &AnswerResult{Data: map[string]any{"severity": "high"}}, nil
	}}

	res, ok, err := a.writeAnswer(context.Background(), AnswerRequest{})

	if err != nil || !ok {
		t.Fatalf("writeAnswer = %v, %v", ok, err)
	}
	if res.Data == nil {
		t.Error("the structured result was dropped")
	}
}

// TestRunDAGSyncAsksForTheAnswer guards the wiring, and it is the test that
// matters most here. Everything above passes on a scheduler that never calls
// writeAnswer at all: the application supplies its answer, the built-in
// aggregator writes prose instead, the run is recorded as completed, and the
// only sign is the shape of the result.
//
// This asked for two call sites when it was written, the second in runDAG. That
// function had no callers and has been deleted, so half of what this guarded
// was a path nothing ran.
//
// Matched loosely on whitespace so gofmt realignment is not a false alarm.
func TestRunDAGSyncAsksForTheAnswer(t *testing.T) {
	src, err := os.ReadFile("scheduler.go")
	if err != nil {
		t.Fatalf("read scheduler.go: %v", err)
	}
	if n := len(regexp.MustCompile(`a\.writeAnswer\(`).FindAll(src, -1)); n != 1 {
		t.Errorf("scheduler.go asks for the supplied answer %d times, want 1 (RunDAGSync)", n)
	}
}

// The structured result is opaque: the engine carries it from the application's
// answer to the caller and never reads it. If the field stops being threaded,
// every caller silently receives nil where its own type used to be.
func TestTheStructuredResultReachesTheCaller(t *testing.T) {
	src, err := os.ReadFile("scheduler.go")
	if err != nil {
		t.Fatalf("read scheduler.go: %v", err)
	}
	if !regexp.MustCompile(`Data:\s+data,`).Match(src) {
		t.Error("SyncResult no longer carries the structured result back to the caller")
	}
}
