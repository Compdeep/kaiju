package agent

import (
	"fmt"
	"log"
	"runtime/debug"
)

// Containing a panic to the goroutine it happened in.
//
// recover() reaches nothing outside its own goroutine, and Go has no global
// handler, so a package that starts goroutines has to guard each one it starts.
// This package starts nineteen. Until these, none of them was guarded: a nil map
// or a bad type assertion anywhere inside a run ended the process, and in an
// application embedding this engine that is the whole daemon.
//
// The capability wrappers are a different thing and both are needed. Those guard
// the points where this package calls the APPLICATION's code, and each one knows
// which capability failed, so it can substitute the right answer and the run
// carries on. These guard everything else — this package's own faults — and all
// they can know is that a goroutine died.
//
// What matters is that recovering is not enough on its own. A goroutine that
// panics has usually promised something to whoever started it, and returning
// without keeping that promise turns a crash into a hang, which is harder to
// diagnose than the crash was. So each guard below completes the promise before
// it returns, and there is one guard per shape of promise rather than one for
// the package.
//
// A recovered panic is still a bug. Every guard logs the stack, which names the
// panic site — checked, not assumed — so these are read and fixed rather than
// absorbed.

/*
 * guardNodeCompletion contains a panic in a goroutine that owes a node
 * completion, and sends the completion it owes.
 * desc: The scheduler counts a node as in flight until exactly one completion
 *       arrives for it, so a guard that only logged would leave the scheduler
 *       waiting on a node that is already dead — the run would stop, having
 *       produced nothing, with no error anywhere.
 *
 *       Sending unconditionally is safe because every send in these functions is
 *       a terminal exit: nothing runs after one, so a panic can only happen
 *       before any send. TestNodeCompletionsAreTerminal holds that, since a
 *       second completion would decrement the in-flight count twice and the run
 *       would conclude with work still running.
 * param: what - the goroutine's name, for the log.
 * param: nodeID - the node this goroutine was firing.
 * param: ch - where the completion goes.
 */
func (a *Agent) guardNodeCompletion(what, nodeID string, ch chan<- nodeCompletion) {
	r := recover()
	if r == nil {
		return
	}
	log.Printf("[dag] %s panicked on node %s: %v\n%s", what, nodeID, r, debug.Stack())
	ch <- nodeCompletion{NodeID: nodeID, Err: fmt.Errorf("%s panicked: %v", what, r)}
}

/*
 * guardLoop contains a panic in a long-lived goroutine that owes nothing.
 * desc: These run for the process's lifetime and nobody waits on them, so there
 *       is nothing to complete — the loop simply stops, and what it was doing
 *       stops with it. That is a real loss and the log says which, because the
 *       symptom afterwards is an absence: no more events, no more heartbeats.
 *
 *       Deliberately not restarted. A loop that panicked once will panic again
 *       on the next iteration that reaches the same state, and a restart turns a
 *       diagnosable stop into a spin.
 * param: what - the loop's name, for the log.
 */
func guardLoop(what string) {
	if r := recover(); r != nil {
		log.Printf("[agent] %s panicked and has stopped: %v\n%s", what, r, debug.Stack())
	}
}
