package agent

import "context"

// The run's identity, available before there is anywhere to put it.
//
// A run has two identifiers. The trigger's id is the caller's own reference and
// it repeats — an application retrying a piece of work hands back the id it was
// given. The run id is this package's, and it is different every time.
//
// Everything that groups a run's output has to use the second: the debug log
// names its file after it, the audit line records it against each tool call,
// the browser groups a run's events by it. Grouping by the caller's reference
// merges two runs into one.
//
// It used to live only on the Graph, which is created some way into a run —
// after admission, after the model calls that route and classify. Those calls
// write traces, so they had nothing to name their file after and used the
// caller's reference instead. Stamping the run on its context at the first
// line means every stage can ask, whether or not a graph exists yet, and the
// ReAct loop — which has no graph at all — can ask too.

type runIDKey struct{}

// withRunID stamps a run's identity on its context. Called once, where the run
// begins.
func withRunID(ctx context.Context, runID string) context.Context {
	if runID == "" {
		return ctx
	}
	return context.WithValue(ctx, runIDKey{}, runID)
}

// runIDFrom returns the run this context belongs to, or empty outside a run.
//
// Empty rather than a fallback to the caller's reference: a trace that cannot
// say which run it came from should say nothing, because naming it after the
// reference puts two runs in one file, which is the fault this exists to fix.
func runIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(runIDKey{}).(string)
	return id
}

// The trigger, on the context for the same reason.
//
// A tool rule is handed the trigger so it can judge a call against what started
// the run. The DAG reads it off the graph, which the ReAct loop does not have —
// so that loop would have handed every rule a nil trigger, which ToolCallRequest
// documents as meaning the call was made outside a run. It was not.

type triggerKey struct{}

// withTrigger stamps what started a run on its context. Called once, beside
// withRunID.
func withTrigger(ctx context.Context, t *Trigger) context.Context {
	if t == nil {
		return ctx
	}
	return context.WithValue(ctx, triggerKey{}, t)
}

// triggerFrom returns what started this run, preferring the graph because that
// is where the DAG keeps it, and falling back to the context for a run that
// builds no graph. Nil outside a run.
func triggerFrom(ctx context.Context, g *Graph) *Trigger {
	if t := triggerOf(g); t != nil {
		return t
	}
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(triggerKey{}).(*Trigger)
	return t
}

// RunIDFrom returns the run a context belongs to, for an application writing
// its own stage.
//
// An application that supplies Handlers.Answer writes the final answer in
// its own function, with its own model call and its own trace — and that trace
// has to name the same run as the engine's, or its entry lands in a different
// file. Exported for that; empty outside a run.
func RunIDFrom(ctx context.Context) string { return runIDFrom(ctx) }
