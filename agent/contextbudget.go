package agent

import "log"

// Sizing the context budget against the deployed model.
//
// Every gate call names a budget in characters — 6000 for guidance, 12000 for
// the evidence an answer is written from, 24000 for the reflector. Those
// numbers carry real judgement about which stage needs more, and they are worth
// keeping. What they cannot carry is the model: the same 12000 characters is
// most of a small window and a rounding error in a large one.
//
// So the relative sizes stay and the scale comes from the model. Nothing
// changes when the window is unknown, which is the common case — most providers
// publish no figure — so a deployment that supplies no catalog behaves exactly
// as before.

const (
	// referenceWindowTokens is the window the existing budgets read as having
	// been chosen for. It is inferred, not documented: the gate's own default
	// budget is 32000 characters, which is a quarter of a 32768-token window at
	// four characters per token, and a quarter of the window for gathered
	// context is a sensible split against the prompt and the reply. A different
	// reference moves every budget by the same factor, so this is the one number
	// to argue with.
	referenceWindowTokens = 32768

	// contextShareOfWindow is the most of a model's window that gathered context
	// may occupy, whatever the scaling arrives at. Evidence competes with the
	// system prompt, the conversation and room to reply.
	contextShareOfWindow = 0.25

	// maxBudgetScale bounds the growth. A million-token window would otherwise
	// multiply every budget by eight and quietly make each call cost eight times
	// as much for evidence nobody asked for. More context is not free and is not
	// always better.
	maxBudgetScale = 4.0

	charsPerToken = 4
)

/*
 * scaleBudget adjusts a gate budget to the deployed model's context window.
 * desc: Returns the budget unchanged when the window is unknown — no catalog
 *       supplied, or a model the catalog does not carry — so this is inert
 *       unless an application opts in through Config.Limits.
 *
 *       Where two lanes run different models the smaller window wins, since one
 *       budget serves whichever stage is asking and overshooting the smaller
 *       model is the failure that matters.
 * param: requested - the budget the caller asked for, in characters.
 * return: the budget to use, in characters.
 */
func (a *Agent) scaleBudget(requested int) int {
	if a == nil || a.cfg.Limits == nil || requested <= 0 {
		return requested
	}
	window := a.smallestKnownWindow()
	if window <= 0 {
		return requested
	}

	windowChars := window * charsPerToken
	scale := float64(windowChars) / float64(referenceWindowTokens*charsPerToken)
	if scale > maxBudgetScale {
		scale = maxBudgetScale
	}

	scaled := int(float64(requested) * scale)
	if ceiling := int(float64(windowChars) * contextShareOfWindow); scaled > ceiling {
		scaled = ceiling
	}
	if scaled == requested {
		return requested
	}
	log.Printf("[gate] budget %d → %d chars (window %d tokens)", requested, scaled, window)
	return scaled
}

// smallestKnownWindow returns the tightest window among the configured lanes, or
// zero when the catalog knows none of them. The executor lane is often a smaller
// model than the reasoning lane, and a budget that fits the larger one overflows
// the smaller.
func (a *Agent) smallestKnownWindow() int {
	// The catalog is an application's to supply, and most do not. Guarded here
	// rather than at each caller: it was checked before the one call that
	// existed, and the second call added later dereferenced a nil func.
	if a == nil || a.cfg.Limits == nil {
		return 0
	}
	smallest := 0
	for _, model := range []string{a.cfg.LLMModel, a.cfg.ExecutorModel} {
		if model == "" {
			continue
		}
		window, _ := a.cfg.Limits(model)
		if window <= 0 {
			continue
		}
		if smallest == 0 || window < smallest {
			smallest = window
		}
	}
	return smallest
}
