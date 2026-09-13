package agent

import (
	"os"
	"strings"
	"testing"
)

// A "continue" that has nothing to run concludes instead of asking again.
//
// This is a decision inside the completion loop, reading local state that never
// leaves the function, so it is asserted against the source in the same way the
// failure branch is — see scheduler_failure_branch_test.go for why.
//
// The defect it guards: "continue" asserts there are steps still to run. When
// nothing launched there are none, and nothing is in flight to produce any, so
// the assertion is contradicted by the graph. It used to be re-asked, with
// workSinceReflection forced to 1 so the "no work" break could not fire while
// it was — which turned one wrong answer into an unbounded loop, because
// re-reflecting resamples the same question on an unchanged graph. On the run
// this was found on, the recorded reflector prompt came back four "continue" to
// four "conclude" over eight sends, so every lap was a fresh coin flip: twelve
// laps, 2m20s and 136k tokens, to reach the answer already in hand before the
// first reflection.
func continueBranch(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("scheduler.go")
	if err != nil {
		t.Fatalf("read scheduler.go: %v", err)
	}
	text := string(src)
	start := strings.Index(text, `case "continue":`)
	if start < 0 {
		t.Fatal(`no "continue" branch in scheduler.go — the shape this test reads has changed`)
	}
	end := strings.Index(text[start:], `case "replan":`)
	if end < 0 {
		t.Fatal(`the "replan" branch no longer follows "continue"`)
	}
	return text[start : start+end]
}

// The loop cannot be re-armed from this branch. workSinceReflection is what the
// scheduler breaks on when nothing has happened since the last reflection;
// setting it here says a tool completed when none did, and a tool completing is
// exactly what schedules the next reflection.
func TestContinueWithNothingToRunDoesNotRearmTheLoop(t *testing.T) {
	branch := continueBranch(t)
	if strings.Contains(branch, "workSinceReflection = 1") {
		t.Error("the continue branch sets workSinceReflection = 1 again — that is the loop: " +
			"it claims a tool completed, and a completed tool is what schedules the next reflection")
	}
	// The quoted literal, so the history in the branch's own comment does not
	// read as the call it is describing.
	if strings.Contains(branch, `"CONTINUE_EMPTY"`) {
		t.Error("the continue branch still records CONTINUE_EMPTY — that state now concludes, " +
			"so nothing should be written under a name that says the run carried on")
	}
}

// And it ends the run the way conclude does, so the aggregator writes the reply
// from the evidence already gathered.
func TestContinueWithNothingToRunConcludes(t *testing.T) {
	branch := continueBranch(t)
	guard := strings.Index(branch, "if inflight == 0 {")
	if guard < 0 {
		t.Fatal("the continue branch no longer tests whether anything launched")
	}
	tail := branch[guard:]
	for _, want := range []string{
		"graph.SkipAllPending()",   // nothing else may start
		"reflectionConcluded = true", // the run ends here
	} {
		if !strings.Contains(tail, want) {
			t.Errorf("a continue with nothing to run must conclude, but the branch is missing %q", want)
		}
	}
}
