package agent

import "context"

// Refining what preflight concluded, and asking when it can't be concluded.
//
// Preflight reads the query and decides how to treat it: chat or work, how much
// authority, which skills. It does that from the text alone, which is all this
// package has.
//
// Two things it therefore cannot do.
//
// It cannot use facts the application holds. "Restart the database on web-1"
// needs to know that web-1 exists; only the application has that list.
//
// And it cannot ask. Today the only way to put a question to the user is for
// the PLANNER to emit a gap, which happens after tools and skills are loaded
// and only if the model chooses to ask rather than guess. Under a request
// shaped like a task, models guess. Preflight is where an ambiguity is
// cheapest to notice and the question costs nothing extra, because the call has
// already happened.
//
// A question is an ordinary reply. The run ends, the question is the answer,
// and the user's next message arrives as the next turn with the exchange in
// History — which preflight already reads. Nothing is held pending, and there
// is no state to resume.

// RefineFunc adjusts what preflight concluded, or replies instead of planning.
//
// It receives the result preflight reached and the trigger it came from, and
// returns one of three things:
//
//   - a refined result — carry on, planning with that instead
//   - a non-empty reply — do not plan; the reply IS the answer, and the user's
//     next message continues the thread
//   - an error — treated as any other preflight failure
//
// The trigger is a pointer because refinement may also settle WHERE the run
// applies — an application that resolves "the database host" to a machine sets
// Target here, before any step is planned against the wrong one.
//
// Returning (nil, "", nil) leaves preflight's own answer standing, so an
// application that only wants to intervene sometimes returns that most of the
// time.
//
// Nil does nothing at all, which is what an application with no such rules
// should get.
//
// It runs after preflight and before any planning, so a reply costs one cheap
// call rather than a plan and a set of tool runs.
type RefineFunc func(ctx context.Context, pf *PreflightResult, t *Trigger) (refined *PreflightResult, reply string, err error)

/*
 * refinePreflight applies the application's refinement, if any.
 * desc: Nil, or an unrefined answer, leaves pf as it stands. A reply stops the
 *       run; the caller returns it as the answer rather than planning.
 * param: ctx - the run's context.
 * param: pf - what preflight concluded.
 * param: t - the trigger; may be adjusted (Target).
 * return: the result to plan with, and a reply that stops the run when set.
 */
func (a *Agent) refinePreflight(ctx context.Context, pf *PreflightResult, t *Trigger) (*PreflightResult, string) {
	if a == nil || a.refine == nil || pf == nil {
		return pf, ""
	}
	refined, reply, err := a.refine(ctx, pf, t)
	if err != nil {
		// The application's refinement failed. Its own answer is unavailable, so
		// preflight's stands — a refinement that cannot run must not stop a run
		// that would otherwise have proceeded.
		return pf, ""
	}
	if reply != "" {
		return pf, reply
	}
	if refined != nil {
		return refined, ""
	}
	return pf, ""
}
