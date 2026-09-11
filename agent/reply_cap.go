package agent

import (
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
 * planFloor is the smallest reply a plan call can be written in.
 * desc: The planner is told it may write up to MaxNodes steps, so a cap below
 *       that count invites a plan that cannot be written. Every other stage
 *       picks a size for how much prose it wants; this one has a stated count
 *       to work from, and it is the only stage that states a minimum.
 *
 *       It is a FLOOR and not the cap. What the plan actually gets is
 *       replyPlanBudget resolved against the model's window, narrowed by the
 *       model's own ceiling and the operator's — see boundsFor. This raises that
 *       number when the run is allowed more nodes than the table expected, and
 *       is otherwise invisible.
 *
 *       planMaxTokens stood here and did the whole job: the configured cap,
 *       doubled when the model reasons, widened to this count when it did not
 *       fit. The doubling was the right instinct and the wrong mechanism — the
 *       reasoning is divided out of the budget explicitly now rather than paid
 *       for by making the whole thing twice as big.
 * return: the least a plan may be given, in tokens.
 */
func (a *Agent) planFloor() int {
	return a.cfg.MaxNodes*stepTokens + planOverhead
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
