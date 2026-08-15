package agent

import "log"

// Run admission.
//
// An application may have reasons of its own for not starting a run that have
// nothing to do with whether the run would succeed: a licence lapsed, a
// maintenance window, a per-tenant quota, an operator pause. This package
// cannot know any of them, so it asks.
//
// Refusal is not failure. A refused run is a decision the application already
// made, so it is reported as a refusal with the application's own wording, not
// as an error — a caller should not have to distinguish "we chose not to" from
// "it broke".

// AdmitFunc decides whether a run may start.
//
// Returning false stops the run before any work or any model call. The reason
// is the application's own wording and is handed back to the caller and written
// to the log, so a refusal explains itself rather than looking like silence. An
// empty reason gets a plain default.
//
// Nil admits everything, which is what an application that has no such rules
// should get — the handler is absent, not broken.
//
// It is consulted once per run, before planning. It is not a substitute for the
// IGX gate: that decides what a run may DO once started, and applies per tool
// call. This decides whether the run starts at all.
type AdmitFunc func(t Trigger) (ok bool, reason string)

// defaultRefusalReason is used when an application refuses without saying why.
const defaultRefusalReason = "this run was not admitted by the host application"

/*
 * admit consults the application's admission check.
 * desc: Nil admits. A false answer with no reason still yields a usable one, so
 *       a caller always has something to report.
 * param: t - the trigger about to run.
 * return: whether to proceed, and the reason when not.
 */
func (a *Agent) admit(t Trigger) (ok bool, reason string) {
	if a == nil || a.admitRun == nil {
		return true, ""
	}
	// A rule that crashed has not refused anything, and refusing on its behalf
	// would stop work the application never said to stop. Admit, as a missing
	// rule does.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[agent] the admission rule panicked, admitting the run: %v", r)
			ok, reason = true, ""
		}
	}()
	ok, reason = a.admitRun(t)
	if ok {
		return true, ""
	}
	if reason == "" {
		reason = defaultRefusalReason
	}
	return false, reason
}
