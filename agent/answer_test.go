package agent

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/skillmd"
)

// An application supplying Answer is asked to write each run's answer.
func TestTheSuppliedAnswerIsUsed(t *testing.T) {
	a := &Agent{answer: func(_ context.Context, _ AnswerRequest) (*AnswerResult, error) {
		return &AnswerResult{Text: "the batch finished with two rejects",
			Labels: map[string]string{"grade": "amber", "kind": "batch"}}, nil
	}}

	res, ok, err := a.writeAnswer(context.Background(), AnswerRequest{})

	if err != nil || !ok {
		t.Fatalf("writeAnswer = %v, %v, %v", res, ok, err)
	}
	if res.Text != "the batch finished with two rejects" {
		t.Errorf("the answer text came back altered: %+v", res)
	}
	if res.Labels["grade"] != "amber" || res.Labels["kind"] != "batch" {
		t.Errorf("the labels came back altered: %+v", res.Labels)
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
		return &AnswerResult{Text: "outcome"}, nil
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

// Assembling the prompt and resolving the run's doctrine both read state this
// package keeps private, so an application cannot do either for itself. Without
// them it can see the graph and the evidence but not the text the built-in
// aggregator would have been given, which is most of what it needs.
func TestTheAnswerIsHandedThePromptAndTheDoctrine(t *testing.T) {
	var seen AnswerRequest
	a := &Agent{
		skillGuidance: map[string]*skillmd.SkillMD{"triage": guidanceSkill("triage", "## Core Principles\nsay what you cannot see\n\n## Aggregator Guidance\nrate it honestly")},
		answer: func(_ context.Context, req AnswerRequest) (*AnswerResult, error) {
			seen = req
			return &AnswerResult{Text: "done"}, nil
		},
	}
	g := NewGraph()
	g.ActiveCards = []string{"triage"}

	if _, ok, err := a.writeAnswer(context.Background(), AnswerRequest{
		Trigger: Trigger{Type: "alert", ID: "a-1"}, Graph: g,
		Evidence: &ContextResponse{Sources: map[string]string{"node_returns": "web-1 is unreachable"}},
	}); !ok || err != nil {
		t.Fatalf("writeAnswer = %v, %v", ok, err)
	}

	if seen.Prompt == "" {
		t.Error("the assembled prompt was not handed over")
	}
	if len(seen.Guidance) != 1 || seen.Guidance[0].Key != "triage" {
		t.Fatalf("the run's doctrine was not handed over: %+v", seen.Guidance)
	}
	// Whole body, not one extracted section: an application choosing a different
	// section, or its own labelling, must still be able to.
	for _, want := range []string{"## Core Principles", "## Aggregator Guidance", "rate it honestly"} {
		if !strings.Contains(seen.Guidance[0].Body, want) {
			t.Errorf("the body was extracted before hand-over; %q is missing", want)
		}
	}
}

// Doctrine registered as a SKILL.md skill cards arrives the same way a
// skill card does. An application registering only skills would otherwise
// be handed nothing, with no sign that anything was missing.
func TestTheDoctrineComesFromBothRegistries(t *testing.T) {
	a := &Agent{
		skillGuidance: map[string]*skillmd.SkillMD{"triage": guidanceSkill("triage", "CARD"), "response": guidanceSkill("response", "SKILL")},
	}
	g := NewGraph()
	g.ActiveCards = []string{"triage", "response", "never_registered"}

	got := a.runGuidance(g)

	if len(got) != 2 {
		t.Fatalf("resolved %d of 2 registered keys: %+v", len(got), got)
	}
	if got[0].Body != "CARD" || got[1].Body != "SKILL" {
		t.Errorf("wrong bodies, or out of the order the run selected: %+v", got)
	}
}

// A run that selected nothing hands over nothing, so the application can tell
// "no doctrine" from "doctrine that happens to be empty".
func TestNoActiveCardsYieldsNoDoctrine(t *testing.T) {
	if got := (&Agent{}).runGuidance(NewGraph()); got != nil {
		t.Errorf("runGuidance = %+v, want nothing", got)
	}
	if got := (&Agent{}).runGuidance(nil); got != nil {
		t.Errorf("a nil graph yielded %+v", got)
	}
}

// The answer a person reads and the line the run record keeps are not always
// the same string. An answer may be pages of markdown while the record wants a
// line that fits a column. Recording the whole answer is not wrong, only
// unreadable, and nothing fails to report it.
func TestTheRecordKeepsTheSummaryWhenThereIsOne(t *testing.T) {
	src, err := os.ReadFile("scheduler.go")
	if err != nil {
		t.Fatalf("read scheduler.go: %v", err)
	}
	text := string(src)
	if !regexp.MustCompile(`recordLine = answered\.Summary`).MatchString(text) {
		t.Error("the run record no longer prefers the answer's summary line")
	}
	if !regexp.MustCompile(`Conclusion\{Outcome: recordLine`).MatchString(text) {
		t.Error("the run record no longer uses the chosen line")
	}
}

// With no summary the answer itself is recorded, so an application that does
// not distinguish the two loses nothing.
func TestTheRecordFallsBackToTheAnswer(t *testing.T) {
	src, err := os.ReadFile("scheduler.go")
	if err != nil {
		t.Fatalf("read scheduler.go: %v", err)
	}
	if !regexp.MustCompile(`if recordLine == "" \{\s*recordLine = outcome`).MatchString(string(src)) {
		t.Error("an answer with no summary line no longer falls back to the answer itself")
	}
}

// Guidance is the exported form: a stage written outside this package needs the
// same doctrine the built-in stages get, and both registries it may live in are
// private. Without it such a stage runs on the generic prompt alone, which is
// not an error — the answer is simply written without the domain knowledge it
// was supposed to have.
func TestGuidanceIsReachableFromOutside(t *testing.T) {
	a := &Agent{
		skillGuidance: map[string]*skillmd.SkillMD{"triage": guidanceSkill("triage", "CARD"), "response": guidanceSkill("response", "SKILL")},
	}

	got := a.Guidance([]string{"response", "triage", "never_registered"})

	if len(got) != 2 {
		t.Fatalf("resolved %d of 2 registered keys: %+v", len(got), got)
	}
	if got[0].Body != "SKILL" || got[1].Body != "CARD" {
		t.Errorf("wrong bodies, or not in the order asked for: %+v", got)
	}
	if len((&Agent{}).Guidance([]string{"triage"})) != 0 {
		t.Error("an agent with nothing registered returned something")
	}
	var nilAgent *Agent
	if nilAgent.Guidance([]string{"triage"}) != nil {
		t.Error("a nil agent returned something")
	}
}
