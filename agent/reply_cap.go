package agent

import (
	"context"
	"log"

	"github.com/Compdeep/kaiju/agent/llm"
)

// Sizing a reply against the model rather than against a constant.
//
// Every call site picks a max_tokens for its own reply — 16 for the router, 256
// for preflight, 16384 for the compute architect. The model never sees that
// number; it is where the provider stops generating. So a number chosen without
// reference to the model is wrong in both directions: too high and the provider
// rejects the request, too low and a reply that had to be parsed arrives cut in
// half.
//
// capReply keeps each site's choice and only corrects it where the model says
// it cannot be met. Config.Limits supplies the numbers; without it nothing here
// changes any call.

// replyFloor is the smallest cap capReply will settle on. A prompt that nearly
// fills the window would otherwise compute a cap of a few tokens, which fails
// in a way that looks like the model refusing to answer.
const replyFloor = 256

// promptHeadroom covers what the estimate cannot see — tool schemas, the
// provider's own framing — so a reply sized against the remaining window does
// not run into the end of it.
const promptHeadroom = 2000

/*
 * capReply lowers a request's reply cap to what the model can actually produce.
 * desc: Two ceilings apply. The model's published maximum reply is the first.
 *       The room left in its context window after the prompt is the second,
 *       estimated at four characters per token, the same rate the context gate
 *       budgets in. The lower of the two wins, and the result is never raised
 *       above what the caller asked for — a site that wants a short reply keeps
 *       getting one.
 *
 *       Nothing happens when Config.Limits is unset, when the model is unknown
 *       to it, or when the request names no model, so an application that never
 *       supplies a catalog is unaffected.
 * param: model - the resolved model id for this call; empty skips the check.
 * param: req - the request, whose MaxTokens is lowered in place.
 */
func (a *Agent) capReply(model string, req *llm.ChatRequest) {
	if a.cfg.Limits == nil || model == "" || req == nil || req.MaxTokens <= 0 {
		return
	}
	contextTokens, maxOutput := a.cfg.Limits(model)
	if contextTokens == 0 && maxOutput == 0 {
		return
	}

	ceiling := req.MaxTokens
	if maxOutput > 0 && maxOutput < ceiling {
		ceiling = maxOutput
	}
	if contextTokens > 0 {
		if room := contextTokens - promptTokens(req.Messages) - promptHeadroom; room < ceiling {
			ceiling = room
		}
	}
	if ceiling < replyFloor {
		ceiling = replyFloor
	}
	if ceiling >= req.MaxTokens {
		return
	}
	log.Printf("[llm] %s: reply cap %d → %d (window %d, max reply %d, prompt ~%d)",
		model, req.MaxTokens, ceiling, contextTokens, maxOutput, promptTokens(req.Messages))
	req.MaxTokens = ceiling
}

// stepTokens is what one planned step costs to write, measured from the two
// worked examples in the EXECUTIVE prompt section: 108 and 142 characters, so
// 125 on average, at four characters per token. Rounded up, because a step
// carrying a long command or goal string costs more than an example does.
const stepTokens = 40

// planOverhead covers the plan call's reply beyond its steps — the intent and
// answer fields, the JSON around the array, and slack for one long parameter.
const planOverhead = 1000

/*
 * planMaxTokens is the reply cap for a plan call.
 * desc: The planner is told it may write up to MaxNodes steps, so the cap has
 *       to fit that many. Every other call site picks a number for how much
 *       prose it wants; this one has a stated count to work from, and a cap
 *       below that count invites a plan that cannot be written.
 *
 *       Raising the configured cap is only safe when the model is known to
 *       accept the larger number — some providers reject a max_tokens above
 *       their own maximum rather than trimming it. So a model the catalog does
 *       not carry, or no catalog at all, keeps the configured cap and behaves
 *       exactly as it did before this existed.
 * param: ctx - carries the per-request lane selection, if any.
 * return: the max_tokens to send with a plan call.
 */
func (a *Agent) planMaxTokens(ctx context.Context) int {
	need := a.cfg.MaxNodes*stepTokens + planOverhead
	if need <= a.cfg.MaxTokens {
		return a.cfg.MaxTokens
	}
	if a.cfg.Limits == nil {
		return a.cfg.MaxTokens
	}
	c, laneModel := a.heavyLane(ctx)
	_, maxOutput := a.cfg.Limits(resolvedModel(laneModel, c))
	switch {
	case maxOutput == 0:
		return a.cfg.MaxTokens
	case maxOutput < need:
		return maxOutput
	default:
		return need
	}
}

/*
 * resolvedModel names the model a call will actually reach.
 * desc: A lane returns an empty model when no per-request selection applies,
 *       and the client fills its own default in later, inside Complete. A
 *       caller that has to size the reply needs the name before that, so it
 *       asks the client directly.
 * param: laneModel - what the lane resolved, empty when it did not.
 * param: c - the client the call will use.
 * return: the model id, or empty when neither knows one.
 */
func resolvedModel(laneModel string, c *llm.Client) string {
	if laneModel != "" {
		return laneModel
	}
	return c.Model()
}

// promptTokens estimates the size of a message list at four characters per
// token. Tool schemas are not counted; promptHeadroom stands in for them.
func promptTokens(messages []llm.Message) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Content)
	}
	return chars / 4
}
