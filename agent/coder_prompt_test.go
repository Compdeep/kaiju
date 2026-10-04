package agent

import (
	"strings"
	"testing"
)

// The prompt has to describe the protocol the schema offers.
//
// A field the schema offers and the prompt never mentions is a field that does not
// get used. `blocked` is the one that matters: it is the coder's only way to decline,
// and without an instruction saying when to reach for it, the model will keep doing
// what it did before — writing a file.
func TestCoderPrompt_ExplainsEveryStatus(t *testing.T) {
	p := buildComputeCoderPrompt("")

	for _, status := range []string{"edited", "created", "no_change", "blocked"} {
		if !strings.Contains(p, status) {
			t.Errorf("the prompt never mentions status %q, so the coder has no reason to use it", status)
		}
	}
	// Not just named — told when to reach for it.
	if !strings.Contains(p, "When to say blocked") {
		t.Error("blocked is offered and never explained")
	}
	for _, cue := range []string{"preserve", "contradict", "below the cut"} {
		if !strings.Contains(p, cue) {
			t.Errorf("the blocked guidance does not cover %q, which is one of the cases that caused the damage", cue)
		}
	}
	// And told who can unblock it, which is what the reflector reads.
	if !strings.Contains(p, `"planner"`) || !strings.Contains(p, `"environment"`) {
		t.Error("the prompt does not say who can unblock a step")
	}
}

// Edits are the shape to prefer, and the prompt has to say so — the coder's default
// was a whole file, and a whole file loses anything it does not reproduce.
func TestCoderPrompt_PrefersEditsAndExplainsLines(t *testing.T) {
	p := buildComputeCoderPrompt("")

	if !strings.Contains(p, "Prefer edits over replacing the whole file") {
		t.Error("nothing tells the coder to prefer edits")
	}
	if !strings.Contains(p, "shown to you in part must be edited, never rewritten") {
		t.Error("nothing forbids rewriting a file seen only in part")
	}
	if !strings.Contains(p, `"lines"`) {
		t.Error("the lines field is not explained")
	}
	if !strings.Contains(p, "refused") {
		t.Error("the prompt does not say what happens to an ambiguous edit")
	}
}

// The summary is the coder's words; the measurement is the engine's. A coder that
// reports its own line count is inventing a number the engine already knows.
func TestCoderPrompt_LeavesTheCountingToTheEngine(t *testing.T) {
	p := buildComputeCoderPrompt("")
	if !strings.Contains(p, "the engine measures those") {
		t.Error("nothing stops the coder reporting its own line or byte counts")
	}
}

// Domain guidance still appends, which is how a skill card reaches the coder.
func TestCoderPrompt_StillTakesDomainGuidance(t *testing.T) {
	p := buildComputeCoderPrompt("always use tabs")
	if !strings.Contains(p, "## Domain Guidance") || !strings.Contains(p, "always use tabs") {
		t.Error("domain guidance no longer reaches the coder")
	}
	if strings.Contains(buildComputeCoderPrompt(""), "## Domain Guidance") {
		t.Error("an empty guidance string still adds the heading")
	}
}
