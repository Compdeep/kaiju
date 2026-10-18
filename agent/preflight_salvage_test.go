package agent

import (
	"strings"
	"testing"
)

// The reply that caused the failure: mode and intent answered correctly on the
// first two lines, then the model degenerated inside "context" and ran to its
// token cap. Unmarshalling into the struct is all-or-nothing, so all of it was
// thrown away — the run fell back to observe-only and the gate refused every
// compute step in the plan.
func TestParsePreflight_KeepsWhatParsed(t *testing.T) {
	raw := `{
      "mode": "agent",
      "intent": "operate",
      "skills": ["data_retrieval", "webdeveloper"],
      "context": {
        "intent": "Retrieve the latest transactions.",
        "selectors”: [“#latest-transactions”, “.transaction-row”]“constants”: [“fetch 50`

	out, err := parsePreflight(raw)
	if err != nil {
		t.Fatalf("a reply that answered mode and intent was rejected outright: %v", err)
	}
	if out.Intent != "operate" {
		t.Errorf("intent = %q, so compute would still be gated", out.Intent)
	}
	if out.Mode != "agent" {
		t.Errorf("mode = %q", out.Mode)
	}
	if len(out.Skills) != 2 {
		t.Errorf("skills = %v", out.Skills)
	}
}

// A clean reply is read exactly as before. The salvage must not change what
// already worked.
func TestParsePreflight_CleanReplyIsUnchanged(t *testing.T) {
	raw := `{"mode":"agent","intent":"observe","skills":["a"],"required_categories":["network"],
	         "compute_mode":"shallow","needs_synthesis":true,
	         "context":{"intent":"do the thing","urls":["https://example.test/a"]}}`
	out, err := parsePreflight(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Mode != "agent" || out.Intent != "observe" || out.ComputeMode != "shallow" || !out.NeedsSynthesis {
		t.Errorf("a clean reply came back changed: %+v", out)
	}
	if len(out.RequiredCategories) != 1 || len(out.Skills) != 1 {
		t.Errorf("lists lost: %+v", out)
	}
}

// Not an object, or an object that decided nothing, is still a failure — the
// caller falls back to defaults, which is right when there is no decision.
func TestParsePreflight_StillFailsWhenNothingWasDecided(t *testing.T) {
	for _, raw := range []string{`not json at all`, `{"skills":["a"]}`, `{}`} {
		if _, err := parsePreflight(raw); err == nil {
			t.Errorf("%q was accepted as a preflight decision", raw)
		}
	}
}

// The point of the salvage, stated as the thing that actually broke: the run's
// intent is what the gate compares a step's impact against, and a lost intent
// falls back to observe-only, which refuses every compute step in the plan.
func TestParsePreflight_SalvagedIntentIsTheOneThatMatters(t *testing.T) {
	out, err := parsePreflight(`{"mode":"agent","intent":"operate","context":{"broken`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Intent != "operate" {
		t.Errorf("intent = %q — compute (impact 100) would be gated at rank 0", out.Intent)
	}
	if !strings.EqualFold(out.Mode, "agent") {
		t.Errorf("mode = %q", out.Mode)
	}
}
