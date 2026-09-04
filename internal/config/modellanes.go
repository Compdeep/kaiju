package config

import (
	"fmt"

	"github.com/Compdeep/kaiju/models"
)

// Whether the model an operator pointed at a lane can drive that lane.
//
// Three of the four lanes force a tool call. The executive pins plan() by name
// (agent/executive.go), and the router and the executor classifiers force theirs
// inside budgets that start at 96 tokens. A model that reasons before it answers
// spends that budget on the reasoning, so the reply arrives empty or arrives too
// late — which reaches an operator as a run that failed, with the model that
// caused it named nowhere.
//
// The catalog already records which models those are, in Info.Thinking and
// Info.ToolCallOK. The model pickers filter on the same two fields, but a config
// file and a custom endpoint reach the lanes without passing a picker, and that
// is the route this reads.
//
// The Answer lane is deliberately absent: it writes prose, and a thinking model
// is a good choice for it.
//
// This warns and does not refuse. The catalog is curated, not exhaustive, so an
// id it does not carry is ordinary, and a daemon that will not start over a name
// it does not recognise is worse than one that says what it thinks.

// laneModel pairs a configured lane with the model id pointed at it.
type laneModel struct {
	lane string
	id   string
}

/*
 * ModelLaneWarnings reports each configured lane whose model the catalog says
 * cannot be relied on for the forced tool call that lane makes.
 * desc: A lane left empty inherits another lane's model, so it is skipped here
 *       and the model behind it is still reported once, under the lane that
 *       names it. A model driving two lanes is reported once for the same
 *       reason — the second line would repeat the first.
 * return: one sentence per unsuitable lane, in lane order. Empty when every
 *         configured model is suitable, or when the catalog carries none of them.
 */
func (c *Config) ModelLaneWarnings() []string {
	lanes := []laneModel{
		{"llm.model (the planner, Holmes and the ReAct loop)", c.LLM.Model},
		{"executor.model (preflight, the reflector and the observer)", c.Executor.Model},
		{"agent.route_model (the chat-or-investigate decision)", c.Agent.RouteModel},
	}
	seen := map[string]bool{}
	var out []string
	for _, l := range lanes {
		if l.id == "" || seen[l.id] {
			continue
		}
		info, known := models.Find(l.id)
		if !known {
			continue
		}
		switch {
		case info.Thinking:
			seen[l.id] = true
			out = append(out, fmt.Sprintf(
				"%s is set to %s, which reasons before it answers. This lane forces a tool call inside a small reply budget, so the reasoning consumes the budget and the call returns empty or times out.",
				l.lane, l.id))
		case !info.ToolCallOK:
			seen[l.id] = true
			out = append(out, fmt.Sprintf(
				"%s is set to %s, which the catalog does not record as reliable at a small forced tool call. This lane makes one.",
				l.lane, l.id))
		}
	}
	return out
}
