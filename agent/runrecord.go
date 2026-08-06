package agent

import (
	"time"

	"github.com/Compdeep/kaiju/agent/gates"
)

// Recording what a run did — including when it did not finish.
//
// A run that fails is a fact about the system, not an absence of one. An
// operator asking "why did nothing happen at 3am" needs the row that says the
// budget ran out, or the planner never produced a plan. Broadcasting a failed
// node to a live view is not the same thing: nobody is watching at 3am, and the
// view is gone by morning.
//
// So every exit records, with a status saying which kind of exit it was.

/*
 * recordRun writes one run to the application's store, if it supplied one.
 * desc: Called at every exit from a run, successful or not. Nothing depends on
 *       the write succeeding — a record that cannot be written must not fail
 *       the work it describes.
 * param: trigger - what started the run.
 * param: startTime - when it started, for the duration.
 * param: graph - the run's graph; nil is tolerated for very early failures.
 * param: budget - the run's budget; nil is tolerated likewise.
 * param: intent - the intent it was gated at.
 * param: verdict - the answer, or the reason it has none.
 * param: status - "completed", "failed", "timeout".
 */
func (a *Agent) recordRun(trigger Trigger, startTime time.Time, graph *Graph, budget *Budget,
	intent gates.Intent, verdict, status string) {
	if a == nil || a.eventStore == nil {
		return
	}

	mode := a.cfg.DAGMode
	if trigger.DAGMode != "" {
		mode = trigger.DAGMode
	}

	var nodes, llmCalls, refCount, investigationCount int
	if graph != nil {
		nodes = graph.NodeCount()
		refCount, investigationCount = graph.ReflectionStats()
	}
	if budget != nil {
		llmCalls = int(budget.LLMCount())
	}

	a.eventStore.InsertRun(Run{
		ID:              trigger.AlertID,
		NodeID:          a.cfg.NodeID,
		TriggerType:     trigger.Type,
		CorrelationID:   trigger.AlertID,
		StartedAt:       startTime.Unix(),
		CompletedAt:     time.Now().Unix(),
		DurationMs:      time.Since(startTime).Milliseconds(),
		Intent:          intent.String(),
		DAGMode:         mode,
		NodesCount:      nodes,
		LLMCalls:        llmCalls,
		ReflectionCount: refCount,
		ReplanCount:     investigationCount, // legacy field name; persisted as `replan_count`.
		Verdict:         verdict,
		Status:          status,
	})
}
