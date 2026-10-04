package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// The result says which of four things happened, and the payload has to agree.
//
// The engine used to infer that from which field arrived: edits, so it edited; code,
// so it wrote. There was no way to say "the file already satisfies this" and no way
// to say "I cannot do this with what you gave me" — and `code` was required, so a
// coder with nothing useful to contribute still had to contribute a file. One handed
// back 2,953 bytes of invented TypeScript over a 2,458-byte Express server, dropping
// four of its five route mounts, and the engine wrote it and logged OK.

// A status that contradicts its own payload is caught, not applied.
func TestCoderResult_ValidateCatchesAContradiction(t *testing.T) {
	cases := map[string]struct {
		in   CoderResult
		says string
	}{
		"edited with no edits": {
			CoderResult{Status: CoderEdited, Summary: "tidied it"},
			"no_change",
		},
		"created with no code": {
			CoderResult{Status: CoderCreated, Summary: "wrote it"},
			"needs its content",
		},
		"no_change with no reason": {
			CoderResult{Status: CoderNoChange},
			"why nothing needed changing",
		},
		"blocked with no reason": {
			CoderResult{Status: CoderBlocked, Summary: "cannot"},
			"what is missing",
		},
		"no status at all": {
			CoderResult{Summary: "did a thing"},
			"no status",
		},
		"a status nobody declared": {
			CoderResult{Status: "rewrote", Summary: "x"},
			"unknown status",
		},
	}
	for name, c := range cases {
		err := c.in.Validate()
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: the error does not say what to do instead: %v", name, err)
		}
	}
}

// The four coherent shapes pass.
func TestCoderResult_ValidateAcceptsEachCompleteAnswer(t *testing.T) {
	for name, in := range map[string]CoderResult{
		"edited": {Status: CoderEdited, Summary: "split two sentences",
			Edits: []EditOp{{OldContent: "a", NewContent: "b"}}},
		"created": {Status: CoderCreated, Summary: "new config",
			Code: json.RawMessage(`"port = 8080"`)},
		"no_change": {Status: CoderNoChange, Summary: "both script tags are already present"},
		"blocked": {Status: CoderBlocked, Summary: "cannot preserve what I cannot see",
			Blocked: &CoderBlockedReason{Needs: "the current contents of main.ts", From: BlockedFromPlanner}},
	} {
		if err := in.Validate(); err != nil {
			t.Errorf("%s: a complete answer was rejected: %v", name, err)
		}
	}
}

// A provider that drops the required status does not fail an otherwise usable
// reply — strict schema, forgiving parser. But a reply carrying nothing at all is
// not read as a no-op: a no-op is a claim about the file, and that reply makes no
// claim about anything.
func TestCoderResult_InferStatusOnlyFromWhatArrived(t *testing.T) {
	edits := CoderResult{Edits: []EditOp{{OldContent: "a", NewContent: "b"}}}
	if !edits.inferStatus() || edits.Status != CoderEdited {
		t.Errorf("edits did not read as edited, got %q", edits.Status)
	}
	code := CoderResult{Code: json.RawMessage(`"x"`)}
	if !code.inferStatus() || code.Status != CoderCreated {
		t.Errorf("code did not read as created, got %q", code.Status)
	}
	blocked := CoderResult{Blocked: &CoderBlockedReason{Needs: "x", From: BlockedFromPlanner}}
	if !blocked.inferStatus() || blocked.Status != CoderBlocked {
		t.Errorf("a reason did not read as blocked, got %q", blocked.Status)
	}

	empty := CoderResult{Summary: "nothing here"}
	if empty.inferStatus() {
		t.Errorf("an empty reply was given the status %q", empty.Status)
	}
	if err := empty.Validate(); err == nil {
		t.Error("an empty reply passed validation")
	}

	// A status that was sent is never overwritten by what the payload looks like.
	explicit := CoderResult{Status: CoderNoChange, Summary: "already fine",
		Code: json.RawMessage(`"this should be ignored"`)}
	if explicit.inferStatus() {
		t.Error("a status that was sent was inferred over")
	}
	if explicit.Status != CoderNoChange {
		t.Errorf("status became %q", explicit.Status)
	}
}

// blocked carries who has to act. "You did not tell me what to preserve" and "I
// could not read the file" want different responses, and as one error string the
// reflector guesses — it has guessed wrong before, twice investigating a timing
// problem that did not exist because the message was a raw filesystem error.
func TestCoderResult_BlockedNamesWhoMustAct(t *testing.T) {
	for _, from := range []string{BlockedFromPlanner, BlockedFromEnvironment} {
		r := CoderResult{Status: CoderBlocked, Summary: "s",
			Blocked: &CoderBlockedReason{Needs: "n", From: from}}
		if err := r.Validate(); err != nil {
			t.Errorf("from=%s rejected: %v", from, err)
		}
	}
	// The schema constrains `from` to those two, and Validate requires `needs`,
	// so a block that names nothing actionable cannot get through.
	r := CoderResult{Status: CoderBlocked, Summary: "s",
		Blocked: &CoderBlockedReason{From: BlockedFromPlanner}}
	if err := r.Validate(); err == nil {
		t.Error("a block with no needs was accepted")
	}
}
