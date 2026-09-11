package agent

import (
	"context"
	"time"

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
	c, laneModel := a.heavyLane(ctx)
	model := resolvedModel(laneModel, c)

	// A model that reasons before answering writes its hidden tokens into the
	// same cap as its visible ones, so the same plan needs roughly twice the
	// room. Measured on one planner prompt: 1,401 completion tokens billed for
	// 470 tokens of visible JSON — two thirds of the generation was thinking.
	//
	// Doubled here rather than in the configured value, so the number an
	// operator sets stays the number a non-thinking model gets.
	base := a.cfg.MaxTokens
	if a.heavyThinks(model) {
		base *= 2
	}

	need := a.cfg.MaxNodes*stepTokens + planOverhead
	if need <= base {
		return base
	}
	if a.cfg.Limits == nil {
		return base
	}
	_, maxOutput := a.cfg.Limits(model)
	switch {
	case maxOutput == 0:
		return base
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

// How long a run may take, against the model that will do its thinking.
//
// The two clocks have to be set together. A single call is bounded by the
// client's request deadline, and the whole run by the wall clock the scheduler
// wraps every call in — so a call deadline the run cannot accommodate is not a
// deadline at all: the run is cancelled by the shorter of the two and the error
// says "context canceled" rather than naming the clock that actually ran out.
//
// A thinking model is given twice the request deadline (llm.thinkingRequestTimeout),
// so the run is given twice the wall clock. Without it, raising one and not the
// other moves the failure rather than removing it.

/*
 * wallClock reports how long this run may take.
 * desc: The configured wall clock, doubled when the model on the reasoning lane
 *       reasons before it answers. That lane's model is the one asked for the
 *       plan, which is the longest single call a run makes; the other lanes
 *       either force reasoning off or write short replies.
 *
 *       Doubling rather than adding, because the thinking is proportional to
 *       the work rather than a fixed overhead: a bigger plan means more of it.
 * return: the wall clock for this run, or zero when none is configured — which
 *         means no wall clock at all, and doubling zero must stay zero.
 */
/*
 * heavyThinks reports whether the reasoning lane will actually reason.
 * desc: The catalog says what a model does by DEFAULT; llmReasoning can say
 *       otherwise for this lane, and the clocks have to size the run that is
 *       really made rather than the one the catalog describes. An operator who
 *       switches reasoning on for a model that ships it off would otherwise get
 *       a thinking run inside a non-thinking budget and clock, which is the
 *       truncated-plan failure this doubling exists to prevent.
 * param: model - the reasoning lane's model id.
 * return: true when reasoning will be part of the reply.
 */
func (a *Agent) heavyThinks(model string) bool {
	switch a.llmReasoning {
	case "on":
		return true
	case "off":
		return false
	}
	return a.cfg.Thinks != nil && model != "" && a.cfg.Thinks(model)
}

func (a *Agent) wallClock() time.Duration {
	base := a.cfg.DAGWallClock
	if base <= 0 {
		return base
	}
	var model string
	if a.llm != nil {
		model = a.llm.Model()
	}
	if a.heavyThinks(model) {
		return base * 2
	}
	return base
}
