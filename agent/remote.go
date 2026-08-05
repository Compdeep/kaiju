package agent

import "context"

// Remote execution.
//
// A plan step may name the machine it runs on. This package holds only that
// idea: a node carries an opaque Target string, and if the embedding
// application supplied an executor, tool nodes with a target are handed to it
// instead of running locally.
//
// Everything that gives the idea meaning stays outside:
//
//   - what a target NAME is (a host, a peer id, a queue, a device serial)
//   - how to REACH one (ssh, http, a p2p transport, a message bus)
//   - how one is CHOSEN (the planner-side logic that decides which machine a
//     step belongs on)
//   - what happens at the far end, including whatever authorisation the
//     receiving side applies to work arriving from elsewhere
//
// So this package gains no transport dependency, and an application's routing
// and trust model remain its own.

// RemoteRequest is one tool call to run somewhere other than here.
//
// A struct rather than positional parameters so an executor can be given more
// context later without breaking every implementation.
type RemoteRequest struct {
	// Target is opaque to this package: it is passed through exactly as the
	// planner set it, and only the executor interprets it.
	Target string
	Tool   string
	Params map[string]any

	// Intent is the IGX intent rank the call was gated at locally. The far end
	// is free to re-derive and re-gate; nothing here assumes it trusts this.
	Intent int

	// CorrelationID ties the call back to whatever caused it. Opaque; this
	// package only passes it along.
	CorrelationID string
}

// RemoteExecutor runs a tool call on another machine.
//
// Implementations live in the embedding application, which owns the transport.
// A nil executor means remote execution is simply unavailable: nodes carrying a
// target then run locally, exactly as before targets existed.
type RemoteExecutor interface {
	Execute(ctx context.Context, req RemoteRequest) (string, error)
}

// TargetValidator rejects a malformed target before a connection is attempted.
//
// This package has no opinion on target syntax, so the check is host-supplied.
// Nil accepts any non-empty target. The returned error is shown to the planner,
// so it should say how to obtain a valid one — a model that invented a target
// needs to be told where real ones come from.
type TargetValidator func(target string) error

/*
 * validateTarget applies the host-supplied validator, if any.
 * param: target - the target to check.
 * return: nil when acceptable.
 */
func (a *Agent) validateTarget(target string) error {
	if a.targetValid == nil {
		return nil
	}
	return a.targetValid(target)
}

/*
 * remoteFor reports whether a node should be dispatched remotely.
 * desc: Only tool nodes are remotable — compute and other LLM-bearing node
 *       types run where the agent runs. A target equal to this node's own id
 *       means "here", so it runs locally.
 * param: n - the node about to fire.
 * return: true when the node should go to the executor.
 */
func (a *Agent) remoteFor(n *Node) bool {
	return n != nil &&
		n.Target != "" &&
		n.Target != a.cfg.NodeID &&
		a.remoteExec != nil &&
		n.Type == NodeTool
}

// TargetLister reports which machines a run concerns.
//
// A run may touch more machines than the one its Target names — the host it
// came from, others the same condition was seen on, one a conversation is
// about. Only the application knows which of those count, so it says.
//
// Used for reporting, not routing: nothing is dispatched from this list. Nil
// falls back to the run's own Target, which is all this package can know.
type TargetLister func(t Trigger) []string

/*
 * runTargets returns the machines a run concerns, for reporting.
 * desc: The application's list when it supplies one, otherwise the trigger's
 *       own target alone. Never nil-panics and never routes.
 * param: t - the trigger.
 * return: the machines, or nil when there are none.
 */
func (a *Agent) runTargets(t Trigger) []string {
	if a != nil && a.targetLister != nil {
		return a.targetLister(t)
	}
	if t.Target != "" {
		return []string{t.Target}
	}
	return nil
}
