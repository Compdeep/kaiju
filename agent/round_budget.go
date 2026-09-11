package agent

import (
	"context"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
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
// The ladder is ours and is enforced here, by a clock. That is why it applies
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

/*
 * roundBudget is how long one round on this lane may take on this run.
 * desc: The run's own effort where it named one, the node's setting otherwise —
 *       the precedence reasoningFor applies at the call seam — lengthened by
 *       whatever the catalog says this lane's model needs.
 *
 *       Floored, never ceilinged. What stops a long run is the operator's own
 *       wall clock, which is a number somebody chose rather than the product of
 *       five others.
 * param: ctx - the run context, which carries the lane selection.
 * param: l - the lane about to be called, whose model sets the allowance.
 * param: t - the run's trigger, which carries this run's effort where it chose one.
 * return: the budget for one round, never below minRoundBudget.
 */
func (a *Agent) roundBudget(ctx context.Context, l Lane, t Trigger) time.Duration {
	effort := t.ReasoningEffort
	if effort == "" {
		effort = a.cfg.LLMReasoningEffort
	}
	d, ok := effortBudget[effort]
	if !ok {
		d = effortBudget[EffortDefault]
	}
	// The floor protects a deadline nobody chose. Fast is chosen, and choosing
	// it is asking for the shorter one.
	if d < minRoundBudget && effort != EffortFast {
		d = minRoundBudget
	}
	// The floor is applied first, so a slow model's allowance is multiplied
	// against the deadline it would actually have been given.
	return llm.ScaleByPace(d, a.cfg.Pace, a.laneModel(ctx, l))
}

// laneModel is the model a lane will send to: the per-run selection where the
// run made one, and the client's own model otherwise — the same order prepare
// resolves them in. Empty when no client is configured for the lane, which is
// what a test agent with no model has.
func (a *Agent) laneModel(ctx context.Context, l Lane) string {
	c, model := a.lane(ctx, l)
	if model != "" {
		return model
	}
	return c.Model()
}
