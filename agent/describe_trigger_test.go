package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// An application's own kind of work, opaque to this package.
type ticketCause struct {
	ID       string
	Severity string
}

func TestDescribeTriggerRendersTheApplicationsOwnWork(t *testing.T) {
	a := &Agent{describeTrigger: func(tr Trigger) string {
		c, ok := tr.Cause.(*ticketCause)
		if !ok {
			return ""
		}
		return "## Ticket " + c.ID + " (" + c.Severity + ")"
	}}

	got := a.formatTrigger(Trigger{Type: "ticket", Cause: &ticketCause{ID: "T-9", Severity: "high"}})
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
	original := &ticketCause{ID: "T-1", Severity: "low"}
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
