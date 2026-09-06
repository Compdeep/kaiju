package agent

import (
	"errors"
	"strings"
	"testing"
)

// A reply can be malformed in two ways at once. The provider that wraps a plan
// in a markdown fence is not a different provider from the one that runs into
// the token cap, and when both happen the fence rung cannot finish and the
// salvager is the only rung left that can.
//
// The ladder falls through for exactly this, so the salvager has to read past
// the fence rather than choke on the backticks it starts with.
func TestAFencedAndCutPlanKeepsItsFinishedSteps(t *testing.T) {
	raw := "```json\n" +
		`{"intent":"operate","steps":[` +
		`{"tool":"web_search","tag":"find","params":{"query":"solana rpc"}},` +
		`{"tool":"web_fetch","tag":"read","params":{"url":`

	var p executiveCallPayload
	if err := parseExecutivePayload(raw, &p); err != nil {
		t.Fatalf("a fenced, cut plan was lost whole: %v", err)
	}
	if len(p.Steps) != 1 {
		t.Fatalf("kept %d step(s), want the one that closed: %+v", len(p.Steps), p.Steps)
	}
	if p.Steps[0].Tag != "find" {
		t.Errorf("kept the wrong step: %+v", p.Steps[0])
	}
	if p.Intent != "operate" {
		t.Errorf("intent = %q, want it carried through the salvage", p.Intent)
	}
}

// A rung that recognised the shape and could not finish stops the ladder, and
// the error says which recovery it was in the middle of. The caller used to get
// the first parser's opinion of a byte, which named neither.
func TestAFailureNamesTheRecoveryItWasAttempting(t *testing.T) {
	// steps really is a string — the shape is recognised — but not JSON.
	raw := `{"intent":"operate","steps":"[{\"tool\": oops}]"}`

	var p executiveCallPayload
	err := parseExecutivePayload(raw, &p)
	if err == nil {
		t.Fatal("a string of broken JSON parsed as steps; it must not")
	}
	if !strings.Contains(err.Error(), "failed to recover plan steps from a double-encoded string") {
		t.Errorf("the error does not name the stage: %v", err)
	}
}

// When no rung applies, the failure still speaks in the same terms rather than
// handing back the raw parser complaint on its own — and it keeps that
// complaint underneath, where it is still the detail worth having.
func TestAnUnrecoverableReplySaysWhatItFailedToDo(t *testing.T) {
	raw := `{"intent":"operate","steps":[{"tool":`

	var p executiveCallPayload
	err := parseExecutivePayload(raw, &p)
	if err == nil {
		t.Fatal("a malformed reply parsed; it must not")
	}
	if !strings.Contains(err.Error(), "failed to recover a plan from the reply") {
		t.Errorf("the error does not say what was attempted: %v", err)
	}
	if errors.Unwrap(err) == nil {
		t.Error("the underlying parse error was dropped; it is the detail worth keeping")
	}
}

// Every exit is one of the two "failed to recover ..." forms. A caller reading
// these into a worklog or a re-plan frame gets a sentence about the stage, never
// a byte offset on its own.
func TestEveryFailureLeavesByTheSameDoor(t *testing.T) {
	unrecoverable := []struct {
		name string
		raw  string
	}{
		{"cut before the first step closed", `{"intent":"operate","steps":[{"tool":`},
		{"not JSON at all", `I could not make a plan for that.`},
		{"steps is a string of nonsense", `{"steps":"not json"}`},
		{"empty reply", ``},
	}
	for _, c := range unrecoverable {
		t.Run(c.name, func(t *testing.T) {
			var p executiveCallPayload
			err := parseExecutivePayload(c.raw, &p)
			if err == nil {
				t.Fatalf("parsed, but nothing here is a plan")
			}
			if !strings.HasPrefix(err.Error(), "failed to recover ") {
				t.Errorf("left by another door: %v", err)
			}
		})
	}
}
