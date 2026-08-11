package agent

import (
	"context"
	"log"
)

// Refusing a tool call for a reason this package cannot know.
//
// The engine already refuses calls on its own terms: the tool's impact against
// the run's intent and the caller's clearance, the scope's allowed list, the
// rate limit. Those are rules about authority, and they are the same whatever
// the application does.
//
// An application has rules of its own. One example: a tool that opens an
// incident may be called by a person asking for one, and may not be called
// during an automatic investigation, because there the incident is opened from
// the finished verdict instead. That rule needs to know what an incident is,
// which this package does not.
//
// It cannot be done by leaving the tool out of the plan, either. Tools are
// filtered when the plan is written, but the reflector, the observer and the
// micro-planner all add steps to a running plan, and those read the whole
// registry. A call can therefore arrive that the planner was never offered.

// AllowToolFunc decides whether a tool call may proceed, after the engine has
// already allowed it.
//
// Returning false stops the call, and the reason is handed back to the model in
// place of a result — so it learns why and can do something else, rather than
// retrying the same call. A refusal is not an error: the run continues.
//
// The function may also fill in a missing parameter by writing to req.Params,
// which is the map the call will run with.
type AllowToolFunc func(ctx context.Context, req ToolCallRequest) (allow bool, reason string)

// ToolCallRequest is one tool call about to run.
//
// A struct rather than positional parameters, so a later field does not break
// every implementation.
type ToolCallRequest struct {
	// Trigger is what started the run. Nil when the call is made outside one.
	Trigger *Trigger

	// Graph is the run so far. Nil when the call is made outside one.
	Graph *Graph

	// Tool is the name the call is for.
	Tool string

	// Params are the arguments it will run with. Writing to this map changes
	// the call.
	Params map[string]any
}

/*
 * allowTool asks the application whether a call may proceed.
 * desc: A nil capability allows everything the engine already allowed, so an
 *       application with no rules of its own pays nothing and behaves exactly
 *       as before this existed.
 * param: ctx - cancelled with the run.
 * param: req - the call about to run.
 * return: whether to proceed, and the reason to hand the model when not.
 */
func (a *Agent) allowTool(ctx context.Context, req ToolCallRequest) (allow bool, reason string) {
	if a == nil || a.allowToolFn == nil {
		return true, ""
	}
	// A rule that crashed has not said yes. Refuse: the call is about to change
	// something, and the model is told why rather than left to retry.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[agent] a tool rule panicked, refusing the call: %v", r)
			allow, reason = false, "The tool "+req.Tool+" is not available for this run."
		}
	}()
	allow, reason = a.allowToolFn(ctx, req)
	if !allow && reason == "" {
		// A refusal with nothing to say leaves the model to guess, and it will
		// guess the same call again. Say something rather than nothing.
		reason = "The tool " + req.Tool + " is not available for this run."
	}
	return allow, reason
}

/*
 * triggerOf returns what started the run a graph belongs to.
 * desc: Nil when there is no graph, or none was recorded — a tool call made
 *       outside a run. A caller deciding on the trigger should treat nil as
 *       unknown rather than as any particular kind of run.
 * param: g - the run, or nil.
 * return: the trigger, or nil.
 */
func triggerOf(g *Graph) *Trigger {
	if g == nil || g.Context == nil {
		return nil
	}
	return g.Context.trigger
}
