package agent

import (
	"context"
	"log"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/llm"
)

// Writing the final answer.
//
// When the graph is finished, runAggregatorWithClient reads the evidence and
// writes a free-text answer for a person to read. That is the right shape for a
// question typed into a chat box, and the wrong shape for a run whose result is
// consumed by code — a severity and a confidence that decide whether an incident
// is raised, a set of indicators another system matches against.
//
// An application that needs the second shape has to write that answer itself: it
// owns the output type, the schema it forces the model to fill, and the wording
// of the request. What it does not own, and should not have to rebuild, is
// everything that happens up to that point — the budget guards, the assembled
// evidence, the coverage edge, the run record.
//
// So the answer is a capability. Supply Answer and it is asked to write each
// run's answer; leave it nil and the built-in aggregator writes every one. The
// function may also decline a single run by returning nothing, which is how an
// application answers some kinds of run itself and lets the built-in aggregator
// handle the rest.

// AnswerFunc writes the final answer for a finished run.
//
// Returning (nil, nil) declines this run: the built-in aggregator writes it
// instead. Return an error only when the answer was meant to be written and
// could not be — the run then fails, exactly as a failure of the built-in
// aggregator does.
type AnswerFunc func(ctx context.Context, req AnswerRequest) (*AnswerResult, error)

// AnswerRequest is everything the engine can hand over about a finished run.
//
// A struct rather than positional parameters, so a later field does not break
// every implementation.
type AnswerRequest struct {
	// Trigger is what started the run. An application decides from this whether
	// the run is one of its own to answer.
	Trigger Trigger

	// Graph is the finished run. The evidence is already assembled in Evidence;
	// the graph is here for an application that wants to read the nodes itself,
	// or call graph.Context.Get with a recipe of its own.
	Graph *Graph

	// Evidence is the assembled node results and worklog, built with the same
	// recipe and budget the built-in aggregator uses.
	Evidence *ContextResponse

	// Intent is the authority the run was gated at.
	Intent gates.Intent

	// History is the conversation so far, empty for a run with none.
	History []llm.Message
}

// AnswerResult is what the application concluded.
type AnswerResult struct {
	// Text is the answer as a person would read it. It is what the caller of
	// RunDAGSync receives as SyncResult.Verdict, what is broadcast, and what is
	// recorded — so an application returning a structured result should still
	// render a readable form here.
	Text string

	// Actions are follow-up actions to hand back to the caller. The engine does
	// not execute them, exactly as with the built-in aggregator's.
	Actions []ActuatorAction

	// Severity and Category are the application's own labels for what the run
	// concluded. They are written to the run record and nowhere else; the engine
	// attaches no meaning to either value and never reads them back.
	Severity string
	Category string

	// Data is the structured result, carried back to the caller on
	// SyncResult.Data and never interpreted. An application casts it back to its
	// own type. Nil when there is nothing beyond the text.
	Data any
}

/*
 * writeAnswer asks the application to write this run's answer.
 * desc: Reports whether it did. A nil capability, or a nil result from it, both
 *       mean it did not, and the caller runs the built-in aggregator — so an
 *       application that answers only some kinds of run says so by returning
 *       nothing, and needs no flag from this package to do it.
 * param: ctx - cancelled with the run.
 * param: req - the finished run.
 * return: the result and true when the application answered; nil and false when
 *         it declined; an error when it tried and failed.
 */
func (a *Agent) writeAnswer(ctx context.Context, req AnswerRequest) (*AnswerResult, bool, error) {
	if a == nil || a.answer == nil {
		return nil, false, nil
	}
	res, err := a.answer(ctx, req)
	if err != nil {
		return nil, false, err
	}
	if res == nil {
		return nil, false, nil
	}
	if res.Text == "" {
		// An answer nobody can read is a bug in the application, not a reason to
		// fail the run — the evidence is gathered and the structured result may
		// still be usable. Say so once, plainly, and carry on.
		log.Printf("[dag] the supplied answer has no text; the caller will see an empty verdict")
	}
	return res, true, nil
}
