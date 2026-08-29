package agent

import (
	"context"
	"log"
	"time"
)

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
// And it cannot ask. The planner returns steps and nothing else, so there is no
// way for it to put a question to the user. Preflight is where an ambiguity is
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

// refineTimeout bounds a refinement, so a slow or wedged one cannot stall every
// run. A refinement typically consults something outside this process — an
// inventory, a directory, a store — and those can hang. Two seconds is long
// enough for a local lookup and short enough that a stuck one is noticed rather
// than waited on.
const refineTimeout = 2 * time.Second

/*
 * refinePreflight applies the application's refinement, if any.
 * desc: Nil, or an unrefined answer, leaves pf as it stands. A reply stops the
 *       run; the caller returns it as the answer rather than planning.
 *
 *       Bounded and isolated: the refinement gets a deadline, and a panic in it
 *       becomes an ordinary failure rather than taking the process down. It is
 *       host code running inside a run, and a run should survive it being wrong.
 * param: ctx - the run's context.
 * param: pf - what preflight concluded.
 * param: t - the trigger; may be adjusted (Target).
 * return: the result to plan with, and a reply that stops the run when set.
 */
func (a *Agent) refinePreflight(ctx context.Context, pf *PreflightResult, t *Trigger) (refinedOut *PreflightResult, replyOut string) {
	if a == nil || a.refine == nil || pf == nil {
		return pf, ""
	}

	// A panic in host code leaves preflight's own answer standing, as any other
	// failure does. Named returns so the recovered path returns something sane.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[preflight] refinement panicked, keeping preflight's answer: %v", r)
			refinedOut, replyOut = pf, ""
		}
	}()

	rctx, cancel := context.WithTimeout(ctx, refineTimeout)
	defer cancel()

	// Run it on its own goroutine and stop waiting when the deadline passes.
	//
	// Handing the deadline to a synchronous call bounds only a refinement that
	// consults it, which is the well-behaved case and not the one worth
	// defending against. A refinement typically reaches something outside this
	// process, and something outside this process is what hangs — so waiting for
	// it to notice is waiting.
	//
	// The goroutine is abandoned rather than stopped, because nothing can stop
	// it. Go has no way to interrupt a function that is not looking. So it gets
	// a COPY of the trigger to write to: a refinement may settle which machine
	// the run is about, and one that returned too late must not write that into
	// a trigger the planner is already reading. The copy is thrown away with the
	// answer, and the fields a refinement is meant to set are values rather than
	// pointers, so nothing it wrote survives.
	type outcome struct {
		refined *PreflightResult
		reply   string
		err     error
	}
	done := make(chan outcome, 1) // buffered: a late goroutine must not block forever
	scratch := *t
	go func() {
		// The recover has to be here too. A panic on this goroutine is not caught
		// by the deferred recover above, which belongs to the caller's.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[preflight] refinement panicked, keeping preflight's answer: %v", r)
				done <- outcome{}
			}
		}()
		refined, reply, err := a.refine(rctx, pf, &scratch)
		done <- outcome{refined, reply, err}
	}()

	select {
	case <-rctx.Done():
		log.Printf("[preflight] refinement did not answer within %s, keeping preflight's own: %v",
			refineTimeout, rctx.Err())
		return pf, ""
	case out := <-done:
		if out.err != nil {
			// The application's refinement failed. Its own answer is unavailable,
			// so preflight's stands — a refinement that cannot run must not stop a
			// run that would otherwise have proceeded.
			return pf, ""
		}
		// It answered in time, so what it wrote to the trigger is wanted.
		*t = scratch
		if out.reply != "" {
			return pf, out.reply
		}
		if out.refined != nil {
			return out.refined, ""
		}
		return pf, ""
	}
}
