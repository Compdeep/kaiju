package agent

import (
	"strings"
	"testing"
)

// A provider that ignores the schema it was sent returns the plan wrapped in a
// markdown block. The JSON inside is complete and correct; only the wrapper is
// wrong, and without this the run dies on "invalid character '`'".
//
// Taken from a real reply: qwen3.6-35b-a3b, one OpenRouter host, reasoning on.
func TestAFencedPlanIsUnwrapped(t *testing.T) {
	raw := "```json\n" + `{
  "answer": "",
  "intent": "operate",
  "steps": [
    {"tool":"web_search","tag":"find","params":{"query":"barycenter"},"depends_on":[],"type":"tool"},
    {"tool":"web_fetch","tag":"read","params":{"url":"${step.find.results.0.url}"},"type":"tool"}
  ]
}` + "\n```"

	var p executiveCallPayload
	if err := parseExecutivePayload(raw, &p); err != nil {
		t.Fatalf("a fenced plan did not parse: %v", err)
	}
	if p.Intent != "operate" {
		t.Errorf("intent = %q, want operate", p.Intent)
	}
	if len(p.Steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(p.Steps))
	}
	if p.Steps[0].Tag != "find" || p.Steps[1].Tag != "read" {
		t.Errorf("wrong steps: %+v", p.Steps)
	}
}

// The fence is not always labelled, and the reply often has a blank line before
// it — both were in the captured replies.
func TestAFencedPlanIsUnwrappedWithoutALanguageTag(t *testing.T) {
	raw := "\n\n```\n" + `{"intent":"observe","answer":"","steps":[{"tool":"file_read","tag":"pam","params":{"path":"/etc/pam.d/cron"}}]}` + "\n```\n"

	var p executiveCallPayload
	if err := parseExecutivePayload(raw, &p); err != nil {
		t.Fatalf("an unlabelled fence did not parse: %v", err)
	}
	if len(p.Steps) != 1 || p.Steps[0].Tag != "pam" {
		t.Fatalf("wrong steps: %+v", p.Steps)
	}
}

// depends_on written as a tag must still resolve after unwrapping — the rung
// links tags like every other rung, against the CLEANED text rather than the
// fenced original, which parses to nothing.
func TestAFencedPlanStillLinksItsTags(t *testing.T) {
	raw := "```json\n" + `{"intent":"operate","answer":"","steps":[
  {"tool":"web_search","tag":"find","params":{"query":"x"},"depends_on":[]},
  {"tool":"web_fetch","tag":"read","params":{"url":"y"},"depends_on":["find"]}
]}` + "\n```"

	var p executiveCallPayload
	if err := parseExecutivePayload(raw, &p); err != nil {
		t.Fatalf("did not parse: %v", err)
	}
	if len(p.Steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(p.Steps))
	}
	if len(p.Steps[1].DependsOn) != 1 {
		t.Fatalf("depends_on lost: %+v", p.Steps[1])
	}
}

// A reply that is simply malformed must still fail. The rung reads the first
// bytes so it only runs on a fence; a broken reply that never had one must not
// be quietly rescued by a text cleaner into something it did not say.
func TestAMalformedPlanWithoutAFenceStillFails(t *testing.T) {
	raw := `{"intent":"operate","steps":[{"tool":`

	var p executiveCallPayload
	err := parseExecutivePayload(raw, &p)
	if err == nil {
		t.Fatal("a malformed reply with no fence parsed; it must not")
	}
	if strings.Contains(err.Error(), "```") {
		t.Errorf("unexpected error mentioning a fence: %v", err)
	}
}

// An unfenced plan is untouched: the common path must not change.
func TestAPlainPlanIsUnaffected(t *testing.T) {
	raw := `{"intent":"observe","answer":"","steps":[{"tool":"file_read","tag":"pam","params":{"path":"/etc/pam.d/cron"}}]}`

	var p executiveCallPayload
	if err := parseExecutivePayload(raw, &p); err != nil {
		t.Fatalf("a plain plan did not parse: %v", err)
	}
	if len(p.Steps) != 1 || p.Steps[0].Tag != "pam" {
		t.Fatalf("wrong steps: %+v", p.Steps)
	}
}
