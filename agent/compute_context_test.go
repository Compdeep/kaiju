package agent

import (
	"strings"
	"testing"
)

// The heading has to be true.
//
// It read "Available Data (from upstream steps)" over whatever was in the
// context slot, including a value no step produced. On the run this was written
// for, a plan wired nothing into a compute and typed one sentence about the date
// into that slot; the coder was shown that sentence under that heading, and
// wrote the figures it did not have as constants labelled with the source they
// should have come from.
func TestComputePrompt_DoesNotClaimUpstreamForTypedValues(t *testing.T) {
	typed := buildComputeUserPrompt(computePrompt{
		Goal:    "calculate the barycenter",
		Context: "Current date: 2026-08-29 13:57:13 UTC",
		Wired:   false,
	})
	if strings.Contains(typed, "wired from earlier steps") {
		t.Error("a value the plan typed in is presented as an earlier step's output")
	}
	if !strings.Contains(typed, "Current date") {
		t.Error("the value itself must still be shown — it is all the stage has")
	}

	wired := buildComputeUserPrompt(computePrompt{
		Goal:    "rank the rows",
		Context: map[string]any{"csv": "a,b,c"},
		Wired:   true,
	})
	if !strings.Contains(wired, "wired from earlier steps") {
		t.Error("data that DID come from a step is no longer said to have")
	}
	if strings.Contains(wired, "No earlier step's output was wired") {
		t.Error("a wired compute is told it has no upstream data")
	}
}

// A compute with nothing wired is told so, and told what to do about it.
//
// Saying the data is absent is not enough on its own: a stage asked for figures
// it does not have will write figures. The instruction that matters is the one
// naming the alternative — report what is missing rather than supply it.
func TestComputePrompt_SaysWhenNothingWasWired(t *testing.T) {
	for _, p := range []computePrompt{
		{Goal: "calculate something"},                                  // no context at all
		{Goal: "calculate something", Context: "a note", Wired: false}, // context, but typed
	} {
		got := buildComputeUserPrompt(p)
		if !strings.Contains(got, "No earlier step's output was wired") {
			t.Errorf("a compute with nothing wired is not told so:\n%s", got)
		}
		if !strings.Contains(got, "say so in the output and name what is missing") {
			t.Errorf("it is told the data is absent and not what to do instead:\n%s", got)
		}
	}
}

// Both modes send the same message, because both build it here. Deep and
// shallow drifting apart is how one of them ends up told something the other is
// not, and the drift is invisible until a run goes wrong on the mode nobody
// tested.
func TestComputePrompt_IsTheOnlyBuilder(t *testing.T) {
	src := readSource(t, "compute.go")
	if n := strings.Count(src, "buildComputeUserPrompt(computePrompt{"); n != 2 {
		t.Errorf("compute.go builds the user prompt %d times, want 2 — one for deep, one for shallow", n)
	}
	if strings.Contains(src, "## Available Data") || strings.Contains(src, "## Goal\\n") {
		t.Error("compute.go assembles prompt sections of its own; they belong in buildComputeUserPrompt")
	}
}
