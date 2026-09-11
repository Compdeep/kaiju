package agent

import (
	"time"
)

/*
 * How long one planning round may take.
 *
 * A deadline is needed because max_tokens does not bound time. A thinking model
 * writes its hidden tokens into the same budget as its reply, so a generous
 * token cap buys a complete plan and an open-ended wait — and on a rig that
 * ignores the cap altogether, an unbounded one.
 *
 * The numbers are measured, on the real 51KB planner prompt, across 52 models:
 *
 *   gemini-3.5-flash-lite   1.6s      gpt-5-nano             45.6s
 *   gpt-5.5                 2.7s      qwen3.8-27b            53.2s
 *   gpt-4.1                 3.0s      kimi-k2.5              54.8s
 *   claude-opus-5           7.0s      gpt-5                  77.3s   <- baseline
 *   deepseek-v4-pro        10.1s      kimi-k3                80.9s
 *   gpt-oss-120b           29.0s      glm-5.3                81.8s
 *   deepseek-v4-flash      38.3s      qwen3-32b             386.1s
 *
 * A flat 120 seconds fits 51 of the 52. The one it does not is qwen3-32b at 386
 * seconds — five times the baseline — and no allowance worth giving would fit
 * it either.
 *
 * A per-model allowance was measured for and rejected on those numbers, because
 * on that prompt only qwen3-32b needed one. Live traffic then disagreed: over
 * 2,577 calls on one deployment, kimi-k2.6 passed 115 seconds on 5 of its 25
 * calls while qwen3.6-35b-a3b did so on 4 of 711. A median close to the
 * baseline and a long tail past the deadline are the same model, and only the
 * tail is cut off.
 *
 * So the allowance is per model after all, held in the catalog as a pace and
 * read here — see models.Info.Pace and docs/model-pace.md. It lengthens this
 * deadline and never shortens it.
 */

// minRoundBudget is the least a round gets, whatever the effort.
//
// gpt-5 is the slowest model anyone would ordinarily choose, at 77.3 seconds
// median over three samples on the planner prompt. A deadline under two minutes
// cuts off the baseline while it is working, and each cut costs a whole second
// call to recover — more than the deadline saved.
const minRoundBudget = 120 * time.Second

// effortBudget is how long a round may take at each effort.
//
// The ladder is ours and is enforced here, by a deadline. That is why it applies
// to every model, unlike reasoning_effort, which only reaches the models the
// catalog records as acting on it.
//
// "low" and "minimal" land on the floor: asking a model to think LESS is a
// different thing from giving the call less time, and two minutes is the least
// a planner call needs on any model measured.
//
// "fast" is the exception, and the only value that goes below the floor. It is
// ours and it says the opposite thing — take less time — so a floor written to
// protect a deadline nobody chose has no business overriding one somebody did.
var effortBudget = map[string]time.Duration{
	EffortFast:    60 * time.Second,
	EffortMinimal: 60 * time.Second,
	EffortLow:     60 * time.Second,
	EffortDefault: 120 * time.Second,
	EffortMedium:  120 * time.Second,
	EffortHigh:    240 * time.Second,
	EffortXHigh:   480 * time.Second,
	EffortMax:     960 * time.Second,
}

// Both functions that stood here — roundBudget, which resolved this ladder
// against a Trigger, and laneModel, which named the model to scale it by — are
// in bounds.go now, as deadlineFor. The move is the point: roundBudget's Trigger
// parameter is why the deadline could only be set by a caller holding a Graph,
// and why three lanes had one and thirteen did not.
