package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Which targets a step may run on, and act on.
//
// A step's tool either requires a target (reads its processes, runs a command
// there) or does not (looks something up, reasons). For one that does, two
// questions are asked before it runs, on this node or another:
//
//   - Does the step name a target? When targets are in use (Handlers.Remote is
//     set) a step that names none is refused. It used to run here, on this
//     node, which for a node that works on other targets is the wrong target
//     with nothing saying so.
//   - Does the run's scope allow that target, at that step's impact?
//     ResolvedScope.Targets holds the answer, set by whoever started the run.
//
// A tool may also act on targets its arguments name (toolapi.TargetSubject),
// wherever it runs. Each of those is held to the same list at the same impact.
//
// The target is a step's Target, or this node when the step names none and
// targets are not in use. A planner writes targets and arguments, so either is
// a request, not a permission: the scope is the permission.

/*
 * checkTarget refuses a step that would run on, or act on, a target it may not.
 * param: n - the node about to fire.
 * param: scope - the run's scope, or nil.
 * return: nil when the step may run; otherwise an error the model reads.
 */
func (a *Agent) checkTarget(n *Node, scope *ResolvedScope) error {
	if n == nil || n.Type != NodeTool {
		return nil
	}
	impact := toolapi.ImpactControl // a tool this node does not know: assume the most
	tool, known := a.registry.Get(n.ToolName)
	if known {
		impact = tool.Impact(n.Params)
	}

	if !known || toolapi.RequiresTarget(tool) {
		target := n.Target
		if target == "" {
			if a.remoteExec != nil {
				return fmt.Errorf("%s requires a target and this step names none, so it was not run: "+
					"set the step's target", n.ToolName)
			}
			target = a.cfg.NodeID
		}
		if err := targetAllowed(scope, target, impact, n.ToolName, "run tools on"); err != nil {
			return err
		}
	}
	if known {
		for _, target := range toolapi.ActsOn(tool, n.Params) {
			if err := targetAllowed(scope, target, impact, n.ToolName, "act on"); err != nil {
				return err
			}
		}
	}
	return nil
}

/*
 * targetAllowed checks one target against the run's target list.
 * param: verb - how the step uses the target, for the message.
 * return: nil when the scope has no list, or lists the target at or above impact.
 */
func targetAllowed(scope *ResolvedScope, target string, impact int, tool, verb string) error {
	if scope == nil || scope.Targets == nil {
		return nil
	}
	ceiling, ok := scope.Targets[target]
	if !ok {
		return fmt.Errorf("this run may not %s %s, so %s was not run; %s",
			verb, target, tool, allowedTargets(scope.Targets))
	}
	if impact > ceiling {
		return fmt.Errorf("on %s this run may use tools up to impact %d, and %s is %d, so it was not run",
			target, ceiling, tool, impact)
	}
	return nil
}

func allowedTargets(m map[string]int) string {
	if len(m) == 0 {
		return "this run may not use any target"
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return "the targets it may use are: " + strings.Join(names, ", ")
}
