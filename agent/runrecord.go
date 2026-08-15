package agent

import (
	"fmt"
	"log"
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
 * param: c - what the run concluded, or the reason it concluded nothing.
 */
func (a *Agent) recordRun(trigger Trigger, startTime time.Time, graph *Graph, budget *Budget,
	intent gates.Intent, c Conclusion) {
	if a == nil || a.eventStore == nil {
		return
	}

	mode := a.cfg.DAGMode
	if trigger.DAGMode != "" {
		mode = trigger.DAGMode
	}

	var nodes, llmCalls, reflections, followUps int
	if graph != nil {
		nodes = graph.NodeCount()
		reflections, followUps = graph.ReflectionStats()
	}
	if budget != nil {
		llmCalls = int(budget.LLMCount())
	}
	if graph == nil && budget == nil {
		nodes, llmCalls = c.Nodes, c.LLMCalls
	}

	runID := trigger.ID
	if graph != nil && graph.RunID != "" {
		runID = graph.RunID
	}

	a.storeRun(Run{
		ID:              runID,
		NodeID:          a.cfg.NodeID,
		TriggerType:     trigger.Type,
		CorrelationID:   trigger.ID,
		Source:          trigger.Source,
		Target:          trigger.Target,
		StartedAt:       startTime.Unix(),
		CompletedAt:     time.Now().Unix(),
		DurationMs:      time.Since(startTime).Milliseconds(),
		Intent:          intent.String(),
		DAGMode:         mode,
		NodesCount:      nodes,
		LLMCalls:        llmCalls,
		ReflectionCount: reflections,
		FollowUpCount:   followUps,
		Outcome:         c.Outcome,
		Labels:          c.Labels,
		Status:          c.Status,
	})
}

// Conclusion is what a run ended with.
//
// A struct rather than a pair of strings, because "aggregator_failed", "failed"
// at a call site says nothing about which is which, and because an application
// that writes its own answer adds labels of its own — see AnswerResult.
type Conclusion struct {
	// Outcome is the answer, or the reason there is none.
	Outcome string
	// Severity and Category are the application's labels for the answer, empty
	// unless it supplied an Answer capability. Passed through untouched.
	Labels map[string]string
	// Status is "completed", "failed", "timeout" or "not_admitted".
	Status string

	// Nodes and LLMCalls are what the run did, for a run that has no graph and
	// no budget to be counted from. The ReAct loop is the only one: it is a flat
	// loop rather than a graph, and it keeps its own tallies.
	//
	// Read only when graph and budget are nil, so there is one source for these
	// numbers and it is the graph wherever a graph exists.
	Nodes    int
	LLMCalls int
}

/*
 * newRunID makes an identifier for one run.
 * desc: The correlation id with the moment the run began, so a retry of the
 *       same cause is a different run and both are recorded. Readable on
 *       purpose: an operator reading an audit row can see which cause it
 *       belongs to without a join.
 * param: correlationID - what caused the run; may be empty.
 * return: the run identifier.
 */
func newRunID(correlationID string) string {
	if correlationID == "" {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%d", correlationID, time.Now().UnixNano())
}

/*
 * runIDOf returns the run an action belongs to.
 * desc: Falls back to the correlation id when there is no graph — a tool call
 *       made outside a run still records something to group by.
 * param: graph - the run, or nil.
 * param: correlationID - the fallback.
 * return: the run identifier.
 */
func runIDOf(graph *Graph, correlationID string) string {
	if graph != nil && graph.RunID != "" {
		return graph.RunID
	}
	return correlationID
}

// Writing to the application's store.
//
// The store is the application's code and it is called at the end of a run, when
// the answer already exists and the graph is finished. So a store that crashes
// costs a row and nothing else — failing the run would throw away work that
// succeeded because the bookkeeping for it did not, which is the same reasoning
// writeAnswer applies when the answering callback crashes.
//
// Both writers log and continue. An application that wants a failed write to be
// loud can make its own implementation say so; this package has no answer worth
// giving beyond the line.

/*
 * storeRun files a finished run, if the application supplied a store.
 * param: r - the run.
 */
func (a *Agent) storeRun(r Run) {
	if a == nil || a.eventStore == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			log.Printf("[agent] the run store panicked recording %s, continuing: %v", r.ID, p)
		}
	}()
	if err := a.eventStore.InsertRun(r); err != nil {
		log.Printf("[agent] could not record run %s: %v", r.ID, err)
	}
}

/*
 * storeAction files one state-changing tool call, if the application supplied a
 * store.
 * param: act - the call that was made.
 */
func (a *Agent) storeAction(act Action) {
	if a == nil || a.eventStore == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			log.Printf("[agent] the action store panicked recording %s, continuing: %v", act.ActionType, p)
		}
	}()
	if err := a.eventStore.InsertAction(act); err != nil {
		log.Printf("[agent] could not record action %s: %v", act.ActionType, err)
	}
}
