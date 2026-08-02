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
 * SetRemoteExec wires an executor for steps that name a machine.
 * desc: Without one, a node's Target is recorded but never acted on.
 * param: r - the executor, or nil to disable remote execution.
 */
func (a *Agent) SetRemoteExec(r RemoteExecutor) { a.remoteExec = r }

/*
 * SetTargetValidator installs a check on target syntax.
 * desc: Runs before the executor is called, so an obviously wrong target costs
 *       a fast local error rather than a connection attempt and a timeout.
 * param: v - the validator, or nil to accept any non-empty target.
 */
func (a *Agent) SetTargetValidator(v TargetValidator) { a.targetValid = v }

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
