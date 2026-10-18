package agent

import (
	"fmt"

	"github.com/Compdeep/kaiju/agent/llm"
)

/*
 * planMeasureLine is one log line per plan reply, written whether or not debug
 * logging is on, so the planner can be measured from an ordinary service log.
 * desc: The questions it answers are the ones asked when a planner misbehaves,
 *       and each took a replay to answer the first time: did the reply stop on
 *       its own or run into the cap; was a shorter plan asked for, and did that
 *       one stop; was the plan read from a cut reply, which means its steps
 *       were salvaged; how many steps, how many distinct tags — a plan whose
 *       steps outnumber its tags is repeating itself — and how many steps came
 *       back typed compute; what the reply cost in tokens, and how much of that
 *       was thinking; and which host served it.
 *
 *       One line, fixed keys, so it greps and so it can be left on.
 * param: round - which plan of the run this is; 0 is the first.
 * param: resp - the reply the plan was read from, which is the shorter one
 *        when the first was cut and a second came.
 * param: first - the first reply's finish reason.
 * param: retry - the shorter reply's finish reason, or "" when none was asked
 *        for; "error" or "no_choices" when it was asked for and did not come.
 * param: parsed - the finish reason of the reply the plan was read from; a
 *        "length" here means the steps were salvaged from a cut reply.
 * param: steps - the plan as read.
 * return: the line, starting "[plan]".
 */
func planMeasureLine(round int, resp *llm.ChatResponse, first, retry, parsed string, steps []PlanStep) string {
	tags := map[string]bool{}
	compute := 0
	for _, s := range steps {
		tags[s.Tag] = true
		if s.Type == "compute" || s.Tool == "compute" {
			compute++
		}
	}
	in, out, think, host := 0, 0, 0, ""
	if resp != nil {
		in, out, host = resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Provider
		if resp.Usage.Details != nil {
			think = resp.Usage.Details.ReasoningTokens
		}
	}
	if retry == "" {
		retry = "-"
	}
	if host == "" {
		host = "-"
	}
	return fmt.Sprintf("[plan] round=%d first=%s retry=%s salvaged=%t steps=%d distinct_tags=%d compute=%d in=%d out=%d think=%d host=%s",
		round, first, retry, parsed == "length", len(steps), len(tags), compute, in, out, think, host)
}
