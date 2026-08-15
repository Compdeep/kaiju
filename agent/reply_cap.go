package agent

import (
	"context"

	"github.com/Compdeep/kaiju/agent/llm"
)

// The planner's reply budget.
//
// Sizing a reply against the model is the client's own business now — see
// llm.Client.Limits, which caps every send. What is left here is the one budget
// this package computes rather than picks: the planner is told it may write up
// to MaxNodes steps, so its cap has to fit that many.

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
