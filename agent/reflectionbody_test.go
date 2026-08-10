package agent

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Phase 1a. Unlike phase 0 this changes behaviour, and the change is a repair.
//
// conclude stored ref.Verdict alone — prose, not JSON. nodeSummary parses a
// reflection node's Result as JSON to build the "decision: reason" trace line,
// so that parse failed and conclude nodes lost their line. continue and
// investigate stored the raw JSON and kept theirs, so the same node type
// behaved differently depending on the branch taken.

const concludeJSON = `{"decision":"conclude","reason":"credential dump confirmed on two hosts",` +
	`"summary":"malicious","verdict":"Confirmed credential theft.","aggregate":false}`

// TestReflectionBodyKeepsTheWholeReflection: everything the reflector decided
// survives, not just the verdict.
func TestReflectionBodyKeepsTheWholeReflection(t *testing.T) {
	ref, err := parseReflectionOutput(concludeJSON)
	if err != nil {
		t.Fatalf("parseReflectionOutput: %v", err)
	}

	b := ReflectionBody{Out: *ref, Raw: concludeJSON}

	if b.Out.Decision != "conclude" {
		t.Errorf("Decision = %q", b.Out.Decision)
	}
	if b.Out.Reason != "credential dump confirmed on two hosts" {
		t.Errorf("Reason = %q", b.Out.Reason)
	}
	if b.Out.Aggregate == nil || *b.Out.Aggregate {
		t.Errorf("Aggregate = %v, want a non-nil false — this was dropped entirely before", b.Out.Aggregate)
	}
	if b.Out.Verdict == "" {
		t.Error("Verdict empty — it must still be reachable")
	}
}

// TestReflectionBodyEvidenceIsTheRawJSON: Evidence is what lands on Node.Result,
// and downstream readers expect the reflector's JSON there.
func TestReflectionBodyEvidenceIsTheRawJSON(t *testing.T) {
	b := ReflectionBody{Raw: concludeJSON}
	if b.Evidence() != concludeJSON {
		t.Errorf("Evidence() = %q, want the raw JSON", b.Evidence())
	}

	// With no raw text, fall back to the summary rather than returning nothing.
	fallback := ReflectionBody{Out: reflectionOutput{Summary: "malicious"}}
	if fallback.Evidence() != "malicious" {
		t.Errorf("fallback Evidence() = %q, want the summary", fallback.Evidence())
	}
}

// TestReflectionBodySummary renders the trace line.
func TestReflectionBodySummary(t *testing.T) {
	cases := []struct {
		name string
		out  reflectionOutput
		want string
	}{
		{"decision and reason", reflectionOutput{Decision: "conclude", Reason: "done"}, "conclude: done"},
		{"summary stands in for a missing reason", reflectionOutput{Decision: "continue", Summary: "still gathering"}, "continue: still gathering"},
		{"no decision leaves the reason alone", reflectionOutput{Reason: "just this"}, "just this"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (ReflectionBody{Out: tc.out}).Summary(); got != tc.want {
				t.Errorf("Summary() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestConcludeRestoresTheTraceLine is the bug this phase fixes, asserted through
// nodeSummary — the function the frontend trace actually uses.
func TestConcludeRestoresTheTraceLine(t *testing.T) {
	ref, err := parseReflectionOutput(concludeJSON)
	if err != nil {
		t.Fatalf("parseReflectionOutput: %v", err)
	}

	// The old way: the verdict alone, which is prose and does not parse as JSON.
	old := &Node{Type: NodeReflection, Result: ref.Verdict}
	if s := nodeSummary(old); strings.HasPrefix(s, "conclude:") {
		t.Fatalf("precondition failed: the old form already produced a decision line (%q), "+
			"so this test cannot show the repair", s)
	}

	// The new way, through the graph so SetBody's rendering is what gets read.
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeReflection})
	g.SetBody(id, ReflectionBody{Out: *ref, Raw: concludeJSON})

	got := nodeSummary(g.Get(id))
	if !strings.HasPrefix(got, "conclude:") {
		t.Errorf("trace line = %q, want it to start with \"conclude:\"", got)
	}
	if !strings.Contains(got, "credential dump") {
		t.Errorf("trace line = %q, want the reflector's reason in it", got)
	}
}

// TestReflectionBodyFieldReadsTheRawJSON: template references against a
// reflection node behave as they did when it stored a plain string.
func TestReflectionBodyFieldReadsTheRawJSON(t *testing.T) {
	b := ReflectionBody{Raw: concludeJSON}

	got, ok := b.Field("decision")
	if !ok || got != "conclude" {
		t.Errorf("Field(\"decision\") = %v, %v; want \"conclude\", true", got, ok)
	}
	if _, ok := b.Field("nope"); ok {
		t.Error("a missing path should miss")
	}

	whole, ok := b.Field("")
	if !ok {
		t.Fatal("empty path should return the whole document")
	}
	if !json.Valid([]byte(whole.(string))) {
		t.Error("empty path should return the raw JSON")
	}
}

// TestSchedulerStoresTheWholeReflection pins the CALL SITE.
//
// The test above proves ReflectionBody and nodeSummary work together, but it
// builds the graph itself — reverting the scheduler to the old flatten leaves
// it passing. Asserted against the source because the defect was which function
// the scheduler called, and a test that rebuilds the call cannot notice the
// call changing.
func TestSchedulerStoresTheWholeReflection(t *testing.T) {
	src, err := os.ReadFile("scheduler.go")
	if err != nil {
		t.Fatalf("read scheduler.go: %v", err)
	}

	// The flatten: storing the verdict alone as a reflection node's result.
	flatten := regexp.MustCompile(`SetResult\([^)]*ref\.Verdict`)
	if loc := flatten.Find(src); loc != nil {
		t.Errorf("scheduler stores ref.Verdict alone again (%q) — decision, reason and "+
			"aggregate are dropped and the conclude trace line breaks", loc)
	}

	// All three reflection branches carry the whole struct.
	if got := strings.Count(string(src), "ReflectionBody{Out: *ref, Raw: comp.Result}"); got != 3 {
		t.Errorf("found %d ReflectionBody stores, want 3 (continue, conclude, investigate)", got)
	}
}
