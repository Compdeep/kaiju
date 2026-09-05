package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// A plan cut at the token cap keeps the steps that finished.
//
// The reply stops mid-token: an open array, an open object, often an open
// string. Everything before that last partial step is whole and was paid for.
// Re-asking for a shorter plan re-derives it at seventeen thousand input tokens.
func TestASalvagedPlanKeepsTheStepsThatFinished(t *testing.T) {
	cut := `{"intent":"observe","answer":"","steps":[` +
		`{"tool":"file_read","tag":"pam","params":{"path":"/etc/pam.d/cron"}},` +
		`{"tool":"get_process","tag":"proc","params":{"pid":1234}},` +
		`{"tool":"file_read","tag":"shad","params":{"path":"/etc/sha`

	got := salvageTruncatedPlan(cut)
	if got == "" {
		t.Fatal("nothing salvaged from a reply with two complete steps")
	}
	var p executiveCallPayload
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatalf("salvaged text does not parse: %v\n%s", err, got)
	}
	if len(p.Steps) != 2 {
		t.Fatalf("kept %d steps, want the 2 that closed", len(p.Steps))
	}
	if p.Steps[0].Tag != "pam" || p.Steps[1].Tag != "proc" {
		t.Errorf("wrong steps kept: %+v", p.Steps)
	}
}

// Cut before the first step closed: nothing to keep, and saying so is what
// sends the caller to the shorter-plan retry.
func TestNothingIsSalvagedWhenNoStepFinished(t *testing.T) {
	for _, cut := range []string{
		`{"intent":"observe","steps":[{"tool":"file_read","tag":"pa`,
		`{"intent":"observe","steps":[`,
		`{"intent":"observe","ans`,
		``,
	} {
		if got := salvageTruncatedPlan(cut); got != "" {
			t.Errorf("salvaged %q from a reply with no complete step: %q", got, cut)
		}
	}
}

// A whole plan is left alone. The array closed on its own, so nothing was cut
// and there is nothing to repair — returning text here would put the salvage in
// the path of every successful plan.
func TestAWholePlanIsNotSalvaged(t *testing.T) {
	whole := `{"intent":"observe","answer":"","steps":[{"tool":"file_read","tag":"pam","params":{}}]}`
	if got := salvageTruncatedPlan(whole); got != "" {
		t.Errorf("a complete plan was treated as cut: %q", got)
	}
}

// A brace inside a string value is text, not structure. Without this the walk
// miscounts depth and cuts in the wrong place — and tool parameters carry
// braces routinely, in ${step.tag.field} references and shell commands.
func TestBracesInsideStringsDoNotConfuseTheWalk(t *testing.T) {
	cut := `{"steps":[` +
		`{"tool":"bash","tag":"a","params":{"command":"awk '{print $1}' /tmp/x"}},` +
		`{"tool":"bash","tag":"b","params":{"command":"echo ${step.a.out} }}}"}},` +
		`{"tool":"bash","tag":"c","params":{"command":"hal`

	got := salvageTruncatedPlan(cut)
	var p executiveCallPayload
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatalf("salvaged text does not parse: %v\n%s", err, got)
	}
	if len(p.Steps) != 2 {
		t.Fatalf("kept %d steps, want 2 — a brace inside a string was counted as structure", len(p.Steps))
	}
	if p.Steps[1].Tag != "b" {
		t.Errorf("cut in the wrong place: %+v", p.Steps)
	}
}

// An escaped quote does not end the string it is in. A plan carrying a Windows
// path or a quoted argument hits this on the first step.
func TestAnEscapedQuoteDoesNotEndTheString(t *testing.T) {
	cut := `{"steps":[` +
		`{"tool":"bash","tag":"a","params":{"command":"echo \"hi {\" there"}},` +
		`{"tool":"bash","tag":"b","params":{"command":"tru`
	got := salvageTruncatedPlan(cut)
	var p executiveCallPayload
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatalf("salvaged text does not parse: %v\n%s", err, got)
	}
	if len(p.Steps) != 1 || p.Steps[0].Tag != "a" {
		t.Fatalf("kept %+v, want the one step that closed", p.Steps)
	}
}

// The parser reaches the salvage on its own, so a cut reply arrives at the
// caller as an ordinary plan rather than as a parse failure.
func TestTheParserSalvagesWithoutBeingAsked(t *testing.T) {
	cut := `{"intent":"observe","answer":"","steps":[` +
		`{"tool":"file_read","tag":"pam","params":{"path":"/etc/pam.d/cron"}},` +
		`{"tool":"file_read","tag":"x","params":{"path":"/etc/sh`
	var p executiveCallPayload
	if err := parseExecutivePayload(cut, &p); err != nil {
		t.Fatalf("the parser failed on a cut plan instead of salvaging it: %v", err)
	}
	if len(p.Steps) != 1 || p.Steps[0].Tag != "pam" {
		t.Fatalf("parsed %+v, want the one step that closed", p.Steps)
	}
}

// Arguments are read from wherever they arrived. A schema reply comes back as
// content and asToolReply moves it into a tool call, emptying Content — so a
// reader that checks only one field finds nothing on exactly the cut replies.
func TestPlanArgumentsReadsEitherField(t *testing.T) {
	fromContent := planArguments(choiceWithContent(`{"steps":[]}`))
	if !strings.Contains(fromContent, "steps") {
		t.Errorf("content was not read: %q", fromContent)
	}
	fromCall := planArguments(choiceWithToolCall(`{"steps":[{"tool":"x"}]}`))
	if !strings.Contains(fromCall, "tool") {
		t.Errorf("tool-call arguments were not read: %q", fromCall)
	}
}

func choiceWithContent(s string) llm.Choice {
	return llm.Choice{Message: llm.Message{Content: s}}
}

func choiceWithToolCall(s string) llm.Choice {
	return llm.Choice{Message: llm.Message{ToolCalls: []llm.ToolCall{
		{Function: llm.FunctionCall{Name: "plan", Arguments: s}},
	}}}
}
