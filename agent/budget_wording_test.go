package agent

import (
	"strings"
	"testing"
)

// The budget line is built in Go rather than in prompts.md, so the frozen
// prompt test does not cover it and a reword can land unnoticed.
//
// It used to read "Budget: max 30 steps, 15 LLM calls." — a ceiling and
// nothing else. Every plan recorded against it came back at 30 or 31 steps,
// and the steps beyond what the objective needed were filled from whatever
// the surrounding context mentioned. Those steps returned evidence about
// unrelated subjects, and the answer was written from all of it, so the
// conclusion described something the objective had not asked about.
//
// The numbers are still enforced elsewhere; this asserts only that the
// wording presents them as a limit to stay under rather than an amount to use.
func TestBudgetLineIsWordedAsACeilingNotATarget(t *testing.T) {
	line := budgetLine(30, 15)

	if !strings.Contains(line, "30") || !strings.Contains(line, "15") {
		t.Fatalf("the budget line no longer states both limits: %q", line)
	}
	if !strings.Contains(line, "ceiling, not a target") {
		t.Error("the budget line does not say the number is a ceiling rather than a target, which is how it was read before")
	}
	if !strings.Contains(line, "did not name") {
		t.Error("the budget line does not rule out steps aimed at subjects the objective never named, which is what the surplus steps did")
	}
	// "max N steps" on its own is the phrasing that produced the behaviour.
	if strings.Contains(line, "max 30 steps") {
		t.Error("the budget line has reverted to stating a bare maximum")
	}
}
