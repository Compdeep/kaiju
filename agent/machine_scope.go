package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Which machines a step may run on, and act on.
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
// A tool may also act on machines its arguments name (toolapi.MachineSubject),
// wherever it runs. Each of those is held to the same list at the same impact.
//
// The machine is a step's target, or this node when the step names none and
// targets are not in use. A planner writes targets and arguments, so either is
// a request, not a permission: the scope is the permission.

/*
 * checkMachine refuses a step that would run on, or act on, a machine it may not.
 * param: n - the node about to fire.
 * param: scope - the run's scope, or nil.
 * return: nil when the step may run; otherwise an error the model reads.
 */
func (a *Agent) checkMachine(n *Node, scope *ResolvedScope) error {
	if n == nil || n.Type != NodeTool {
		return nil
	}
	impact := toolapi.ImpactControl // a tool this node does not know: assume the most
	tool, known := a.registry.Get(n.ToolName)
	if known {
		impact = tool.Impact(n.Params)
	}

	if !known || toolapi.RequiresTarget(tool) {
		machine := n.Target
		if machine == "" {
			if a.remoteExec != nil {
				return fmt.Errorf("%s runs on a machine and this step names none, so it was not run: "+
					"set the step's target to the machine it is for", n.ToolName)
			}
			machine = a.cfg.NodeID
		}
		if err := machineAllowed(scope, machine, impact, n.ToolName, "run tools on"); err != nil {
			return err
		}
	}
	if known {
		for _, machine := range toolapi.ActsOn(tool, n.Params) {
			if err := machineAllowed(scope, machine, impact, n.ToolName, "act on"); err != nil {
				return err
			}
		}
	}
	return nil
}

/*
 * machineAllowed checks one machine against the run's machine list.
 * param: verb - how the step uses the machine, for the message.
 * return: nil when the scope has no list, or lists the machine at or above impact.
 */
func machineAllowed(scope *ResolvedScope, machine string, impact int, tool, verb string) error {
	if scope == nil || scope.Machines == nil {
		return nil
	}
	ceiling, ok := scope.Machines[machine]
	if !ok {
		return fmt.Errorf("this run may not %s %s, so %s was not run; %s",
			verb, machine, tool, allowedMachines(scope.Machines))
	}
	if impact > ceiling {
		return fmt.Errorf("on %s this run may use tools up to impact %d, and %s is %d, so it was not run",
			machine, ceiling, tool, impact)
	}
	return nil
}

func allowedMachines(m map[string]int) string {
	if len(m) == 0 {
		return "this run may not use any machine"
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return "the machines it may use are: " + strings.Join(names, ", ")
}
