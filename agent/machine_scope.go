package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Which machine a step may run on.
//
// A step's tool either runs on a machine (reads its processes, runs a command
// there) or does not (looks something up, reasons). For one that does, two
// questions are asked before it runs, on this machine or another:
//
//   - Does the step name a machine? When targets are in use (Handlers.Remote is
//     set) a step that names none is refused. It used to run here, on this
//     node, which for a node that works on other machines is the wrong machine
//     with nothing saying so.
//   - Does the run's scope allow that machine, at that step's impact?
//     ResolvedScope.Machines holds the answer, set by whoever started the run.
//
// The machine is a step's target, or this node when the step names none and
// targets are not in use. A planner writes targets, so a target is a request,
// not a permission: the scope is the permission.

/*
 * checkMachine refuses a step that would run on a machine it may not.
 * param: n - the node about to fire.
 * param: scope - the run's scope, or nil.
 * return: nil when the step may run; otherwise an error the model reads.
 */
func (a *Agent) checkMachine(n *Node, scope *ResolvedScope) error {
	if n == nil || n.Type != NodeTool {
		return nil
	}
	impact := toolapi.ImpactControl // a tool this node does not know: assume the most
	if tool, ok := a.registry.Get(n.ToolName); ok {
		if !toolapi.RequiresTarget(tool) {
			return nil
		}
		impact = tool.Impact(n.Params)
	}

	machine := n.Target
	if machine == "" {
		if a.remoteExec != nil {
			return fmt.Errorf("%s runs on a machine and this step names none, so it was not run: "+
				"set the step's target to the machine it is for", n.ToolName)
		}
		machine = a.cfg.NodeID
	}
	if scope == nil || scope.Machines == nil {
		return nil
	}
	ceiling, ok := scope.Machines[machine]
	if !ok {
		return fmt.Errorf("this run may not run tools on %s, so %s was not run; %s",
			machine, n.ToolName, allowedMachines(scope.Machines))
	}
	if impact > ceiling {
		return fmt.Errorf("on %s this run may use tools up to impact %d, and %s is %d, so it was not run",
			machine, ceiling, n.ToolName, impact)
	}
	return nil
}

func allowedMachines(m map[string]int) string {
	if len(m) == 0 {
		return "this run may not run tools on any machine"
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return "the machines it may use are: " + strings.Join(names, ", ")
}
