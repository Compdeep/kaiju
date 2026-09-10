package agent

import "time"

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
 * That is why there is no per-model scaling here. It was measured for, and the
 * measurement said it changes the outcome for one pathological model, from cut
 * off to cut off.
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
var effortBudget = map[string]time.Duration{
	EffortMinimal: 60 * time.Second,
	EffortLow:     60 * time.Second,
	EffortDefault: 120 * time.Second,
	EffortMedium:  120 * time.Second,
	EffortHigh:    240 * time.Second,
	EffortXHigh:   480 * time.Second,
	EffortMax:     960 * time.Second,
}

/*
 * roundBudget is how long one planning round may take on this run.
 * desc: The run's own effort where it named one, the node's setting otherwise —
 *       the precedence reasoningFor applies at the call seam.
 *
 *       Floored, never ceilinged. What stops a long run is the operator's own
 *       wall clock, which is a number somebody chose rather than the product of
 *       five others.
 * param: t - the run's trigger, which carries this run's effort where it chose one.
 * return: the budget for one round, never below minRoundBudget.
 */
func (a *Agent) roundBudget(t Trigger) time.Duration {
	effort := t.ReasoningEffort
	if effort == "" {
		effort = a.cfg.LLMReasoningEffort
	}
	d, ok := effortBudget[effort]
	if !ok {
		d = effortBudget[EffortDefault]
	}
	if d < minRoundBudget {
		return minRoundBudget
	}
	return d
}
