package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// An application's own kind of work, opaque to this package.
type ticketCause struct {
	ID     string
	Rating string
}

func TestDescribeTriggerRendersTheApplicationsOwnWork(t *testing.T) {
	a := &Agent{describeTrigger: func(tr Trigger) string {
		c, ok := tr.Cause.(*ticketCause)
		if !ok {
			return ""
		}
		return "## Ticket " + c.ID + " (" + c.Rating + ")"
	}}

	got := a.formatTrigger(Trigger{Type: "ticket", Cause: &ticketCause{ID: "T-9", Rating: "high"}})
	if !strings.Contains(got, "Ticket T-9") {
		t.Fatalf("the application's rendering did not reach the planner: %q", got)
	}
}

// The point of returning "": an application handles what it knows and leaves
// everything else to the built-in rendering.
func TestUnrecognisedCauseFallsThroughToDefault(t *testing.T) {
	a := &Agent{describeTrigger: func(tr Trigger) string {
		if _, ok := tr.Cause.(*ticketCause); !ok {
			return "" // not mine
		}
		return "should not appear"
	}}

	got := a.formatTrigger(Trigger{
		Type: "chat_query",
		Data: json.RawMessage(`{"query":"what is running?"}`),
	})
	if got != "what is running?" {
		t.Fatalf("expected the built-in chat rendering, got %q", got)
	}
}

// The built-in wording is what every application without a DescribeTrigger
// gets, so it must say only what this package knows: a run started, something
// caused it, here is what came with it. Its headings and its instruction used
// to name one kind of payload, which told every model driven by this loop what
// its input was.
func TestTheBuiltInWordingClaimsNothingAboutThePayload(t *testing.T) {
	a := &Agent{}
	got := a.formatTrigger(Trigger{
		Type:   "event",
		ID:     "corr-9",
		Source: "somewhere",
		Data:   json.RawMessage(`{"k":"v"}`),
	})

	for _, word := range []string{"Alert", "alert", "Investigate", "threat", "incident"} {
		if strings.Contains(got, word) {
			t.Errorf("the built-in wording says %q, which is one product's word for what a run is about:\n%s", word, got)
		}
	}
	// It still has to carry the three things it knows, or an application
	// without a description loses the context entirely.
	for _, part := range []string{"event", "corr-9", "somewhere", `{"k":"v"}`} {
		if !strings.Contains(got, part) {
			t.Errorf("the built-in wording dropped %q:\n%s", part, got)
		}
	}
}

func TestNoCallbackIsUnchangedBehaviour(t *testing.T) {
	a := &Agent{}
	got := a.formatTrigger(Trigger{
		Type: "chat_query",
		Data: json.RawMessage(`{"query":"hello"}`),
	})
	if got != "hello" {
		t.Fatalf("nil callback must behave exactly as before: %q", got)
	}
}

// Cause is carried untouched — this package must never interpret it.
func TestCauseIsCarriedUnmodified(t *testing.T) {
	original := &ticketCause{ID: "T-1", Rating: "low"}
	var seen any
	a := &Agent{describeTrigger: func(tr Trigger) string {
		seen = tr.Cause
		return "x"
	}}
	a.formatTrigger(Trigger{Cause: original})
	if seen != any(original) {
		t.Error("Cause did not reach the callback as the same value")
	}
}
