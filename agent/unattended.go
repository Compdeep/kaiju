package agent

// Whether anyone is watching a run.
//
// It changes what the engine will do. A run somebody is waiting on may ask a
// question and may use tools that record a person's judgement. A run with
// nobody watching may do neither — there is no one to answer, and no judgement
// to record.
//
// Trigger.ExecutionMode is how an application says so, and that is the default
// answer. But an application may know it from something else: its own kind of
// work, where the request arrived from, a schedule. Rather than require every
// construction site to remember to set the field, it can answer the question
// directly.

// UnattendedFunc reports whether a run has nobody watching it.
//
// Nil uses the built-in answer: the run is unattended when its ExecutionMode
// says autonomous. An application that supplies one replaces that entirely, so
// it should include the ExecutionMode check if it still wants it — a
// replacement that silently kept engine behaviour would be harder to reason
// about than one that states everything it means.
type UnattendedFunc func(t Trigger) bool

/*
 * unattended reports whether nobody is watching this run.
 * desc: The application's answer when it supplied one, otherwise the run's own
 *       ExecutionMode.
 * param: t - the trigger.
 * return: true when nobody is watching.
 */
func (a *Agent) unattended(t Trigger) bool {
	if a != nil && a.isUnattended != nil {
		return a.isUnattended(t)
	}
	return t.ExecutionMode == "autonomous"
}
