package agent

import (
	"fmt"
	"strings"

	"github.com/Compdeep/kaiju/agent/gates"
)

// Refusing a run the caller asked for at a rank it cannot be done at.
//
// A rank says what a run may do: read only, make reversible changes, or make
// irreversible ones. Where the engine chooses it, a plan needing more is a
// question the engine can answer itself — the rank moves to meet the work, and
// clearance and scope remain the ceiling.
//
// Where the CALLER chose it, it is not a question. They have said what this run
// may do, and a plan that needs more cannot be run as asked.
//
// That used to be discovered mid-run: the plan was built, the run started, and
// the first step over the line failed at the gate with
//
//	gate: compute blocked (impact=100 > min(intent=rank(0), clearance=100, scope=-1) = 0)
//
// which arrived after five steps had already succeeded, named no remedy, and
// left an answer half-assembled. The work was done, the money spent, and the
// one step that mattered refused. It is knowable before any of that: the plan
// says which tools it will call, and each tool says what it costs.

// IntentGapError is a run refused because the plan needs a rank the caller did
// not grant. Nothing has run when this is returned.
type IntentGapError struct {
	Needed  gates.Intent // the rank the plan requires
	Allowed gates.Intent // the rank this run was given
	Steps   []string     // the steps that need more, named as the plan named them
}

func (e *IntentGapError) Error() string {
	return fmt.Sprintf("this run is limited to %s, and %s needs %s",
		e.Allowed, e.stepList(), e.Needed)
}

// Message is what a person is told. Names the rank, the steps and the remedy —
// the gate's own wording names an arithmetic comparison and no way forward.
func (e *IntentGapError) Message() string {
	return fmt.Sprintf("This request needs permission to %s, and the run was limited to %s. "+
		"%s cannot run at that level. Re-run it allowing %s.",
		e.Needed, e.Allowed, e.stepList(), e.Needed)
}

func (e *IntentGapError) stepList() string {
	switch len(e.Steps) {
	case 0:
		return "a step"
	case 1:
		return "the step " + e.Steps[0]
	}
	return "the steps " + strings.Join(e.Steps, ", ")
}

/*
 * validatePlanIntent refuses a plan that needs more than the run was granted.
 * desc: Reads the impact each planned step will be gated at, the same way the
 *       dispatcher will read it, and compares the highest against the rank this
 *       run holds. Returns nil when the plan fits, which is the ordinary case.
 *
 *       Only for a rank the CALLER pinned. Where the engine chose the rank,
 *       a plan needing more is the engine disagreeing with itself and is
 *       resolved rather than refused — see reconcileComputeIntent.
 * param: nodes - the plan, already resolved to nodes so each names a real tool.
 * param: trigger - what started the run, for whether the rank is the caller's.
 * param: intent - the rank the run holds.
 * return: the gap, or nil.
 */
func (a *Agent) validatePlanIntent(nodes []*Node, trigger Trigger, intent gates.Intent) *IntentGapError {
	if a == nil || a.registry == nil || a.intentRegistry == nil {
		return nil
	}
	if trigger.Intent() == gates.IntentAuto {
		return nil // the engine's own rank; it is not refusing itself
	}

	gap := &IntentGapError{Allowed: intent}
	for _, n := range nodes {
		if n == nil || n.ToolName == "" {
			continue
		}
		tool, ok := a.registry.Get(n.ToolName)
		if !ok {
			continue // an unknown tool is dropped elsewhere, not refused here
		}
		needs := gates.Intent(a.intentRegistry.ResolveToolIntent(n.ToolName, tool, n.Params))
		if needs <= intent {
			continue
		}
		if needs > gap.Needed {
			gap.Needed = needs
		}
		gap.Steps = append(gap.Steps, stepLabel(n))
	}
	if len(gap.Steps) == 0 {
		return nil
	}
	return gap
}
