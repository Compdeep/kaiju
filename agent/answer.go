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

	// Prompt is the request and the evidence rendered as the text the built-in
	// aggregator would have been given. Supplied because assembling it reads
	// state this package keeps private; an application may ignore it and build
	// its own from Graph and Evidence.
	Prompt string

	// Guidance is the doctrine this run selected: one entry per card or skill,
	// with its key and its whole body.
	//
	// Whole, and unextracted on purpose. The built-in aggregator takes the
	// "## Aggregator Guidance" section and nothing else; an application that
	// wants a different section, or several, or its own labelling around them,
	// would have that choice made for it by anything narrower — and the
	// difference would show up as a quietly reworded prompt, not as a failure.
	Guidance []SkillCard
}

// AnswerResult is what the application concluded.
type AnswerResult struct {
	// Text is the answer as a person would read it. It is what the caller of
	// RunDAGSync receives as SyncResult.Outcome, what is broadcast, and what is
	// recorded — so an application returning a structured result should still
	// render a readable form here.
	Text string

	// Summary is the one line the run record keeps. Empty means use Text.
	//
	// Separate because the two are not always the same string: an answer a
	// person reads may be pages of markdown, while the record wants a line
	// that fits a column and a list. Recording the whole answer is not wrong,
	// only unreadable, and it is the kind of change nothing fails to report.
	Summary string

	// Actions are follow-up actions to hand back to the caller. The engine does
	// not execute them, exactly as with the built-in aggregator's.
	Actions []ActuatorAction

	// Labels are the application's own labels for what the run concluded. They
	// are written to the run record and nowhere else; this package attaches no
	// meaning to any key or value and never reads one back.
	//
	// A map rather than named fields. It was Severity and Category, which named
	// what one product happened to label a run with and left every other
	// application putting its own idea into a field called Severity. Nothing
	// here ever read either one, so there was nothing for the names to earn.
	Labels map[string]string

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
func (a *Agent) writeAnswer(ctx context.Context, req AnswerRequest) (resOut *AnswerResult, okOut bool, errOut error) {
	if a == nil || a.answer == nil {
		return nil, false, nil
	}
	// Derived from what the engine already holds, and only once something is
	// there to read them: a run with no Answer capability returned above and
	// pays nothing for either.
	req.Prompt = a.assembleAggregatorPrompt(req.Trigger, req.Graph, req.Evidence)
	req.Guidance = a.runGuidance(req.Graph)

	// A panic means the application did not write an answer, so the built-in
	// aggregator writes it. Failing the run instead would throw away a finished
	// graph over a fault in the last step.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[dag] the supplied answer panicked, the aggregator will write it: %v", r)
			resOut, okOut, errOut = nil, false, nil
		}
	}()

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
		log.Printf("[dag] the supplied answer has no text; the caller will see an empty outcome")
	}
	return res, true, nil
}

/*
 * runGuidance collects the doctrine a run selected, key and body together.
 * desc: Resolves each key the one way keys are resolved, through
 *       lookupGuidanceBody, so a card and a SKILL.md skill cards both
 *       arrive and the caller cannot tell which registry either came from.
 *       A key nothing registered contributes nothing.
 * param: graph - the run; nil or no active cards yields nothing.
 * return: one entry per key that resolved, in the order the run selected them.
 */
func (a *Agent) runGuidance(graph *Graph) []SkillCard {
	if graph == nil {
		return nil
	}
	return a.Guidance(graph.ActiveCards)
}

/*
 * Guidance collects the doctrine registered under the given keys.
 * desc: Exported because a stage written outside this package needs the same
 *       text the built-in stages get, and the two registries it may live in —
 *       skill cards — are both private. A key
 *       nothing registered contributes nothing.
 *
 *       Whole bodies, unextracted: which section a stage reads is the stage's
 *       own business, and anything narrower would make that choice for it.
 * param: keys - the keys to resolve, in the order they should appear.
 * return: one entry per key that resolved.
 */
func (a *Agent) Guidance(keys []string) []SkillCard {
	if a == nil || len(keys) == 0 {
		return nil
	}
	var out []SkillCard
	for _, key := range keys {
		if body, name := a.lookupGuidanceBody(key); body != "" {
			out = append(out, SkillCard{Key: name, Body: body})
		}
	}
	return out
}
