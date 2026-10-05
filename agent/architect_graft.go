package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// What runs and checks a coder's output is grafted once the coder has produced
// some, which is how the shallow path has always worked.
//
// The architect used to graft everything at the moment it finished planning: its
// coders, then the execute and service nodes wired to every one of them, then the
// validation checks wired to all of those. Those run nodes carry a literal command
// and read no template, so nothing held them back from a coder that failed —
// blockedByDep is raised only by resolveTemplateField, for a template reading an
// empty result — and IsTerminal counts StateFailed. A coder that wrote no file left
// its service node ready, and the service started against a file that was never
// written. A prune in the failure path undid that, which meant compute failures
// behaved differently in deep than in shallow for no reason the two paths disagreed
// about.
//
// Grafting late removes the need for it. graftComputeExecution reads comp.Result
// and fires only when it carries an execute command, so a shallow compute that
// fails has no exec child — nothing to stop, nothing to prune. This gives the
// architect the same property: a coder that failed produces no run node, because
// the node is created from the result rather than from the plan.

// architectResult is the architect's reply. Named rather than inline because both
// the graft that reads its coders and the graft that reads its run commands parse
// the same payload, one when the architect finishes and one when its coders do.
type architectResult struct {
	Type        string          `json:"type"`
	ProjectRoot string          `json:"project_root,omitempty"`
	Setup       []string        `json:"setup,omitempty"`
	FollowUp    json.RawMessage `json:"follow_up,omitempty"`
	Execute     string          `json:"execute,omitempty"`
	Services    []architectService
	Validation  []architectCheck
}

type architectService struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Workdir string `json:"workdir,omitempty"`
	Port    int    `json:"port,omitempty"`
}

type architectCheck struct {
	Name   string `json:"name"`
	Check  string `json:"check"`
	Expect string `json:"expect"`
}

/*
 * coderChildren returns the architect's coder nodes, in graft order.
 * desc: Its compute children and nothing else. The setup nodes it grafts are bash
 *       and the run nodes it will graft are bash or service, so type is enough to
 *       tell them apart without tag matching.
 * return: the coder children, empty when there are none.
 */
func coderChildren(graph *Graph, archID string) []*Node {
	var out []*Node
	arch := graph.Get(archID)
	if arch == nil {
		return nil
	}
	for _, cid := range arch.Children {
		if n := graph.Get(cid); n != nil && n.Type == NodeCompute && n.SpawnedBy == archID {
			out = append(out, n)
		}
	}
	return out
}

/*
 * graftArchitectRunNodes grafts what runs and checks the coders' output.
 * desc: Called each time one of the architect's coder children reaches a terminal
 *       state, and does nothing until all of them have. A coder that failed means
 *       nothing is grafted at all: its siblings' files may be fine, but the run
 *       commands were planned against the whole set, and starting a server that
 *       imports a file nobody wrote is the failure this exists to prevent.
 * param: arch - the architect compute node.
 * param: budget - node spawn budget; services are grafted outside it, as before.
 * return: nothing. It logs what it grafted, and the worklog carries what it did not.
 */
func (a *Agent) graftArchitectRunNodes(graph *Graph, arch *Node, budget *Budget) {
	if arch == nil || arch.RunGrafted {
		return
	}
	children := coderChildren(graph, arch.ID)
	if len(children) == 0 {
		return
	}

	var failed []string
	for _, c := range children {
		if !c.IsTerminal() {
			return // still coding
		}
		if c.State != StateResolved {
			failed = append(failed, c.Tag)
		}
	}
	arch.RunGrafted = true

	if len(failed) > 0 {
		// In the worklog because that is what the reflector reads. Nothing was
		// grafted, so there is no node whose absence explains itself.
		log.Printf("[dag] architect %s: %d of %d coders did not resolve (%s) — nothing to run",
			arch.Tag, len(failed), len(children), strings.Join(failed, ", "))
		appendWorklog(a.cfg.MetadataDir, graph.SessionID, arch.Tag, "NOT_RUN",
			fmt.Sprintf("%d of %d coders did not write their file (%s), so the run and check steps were not created",
				len(failed), len(children), strings.Join(failed, ", ")))
		return
	}

	var cr architectResult
	if err := json.Unmarshal([]byte(computePayload(arch.Result)), &cr); err != nil {
		log.Printf("[dag] architect %s: cannot re-read its plan to graft run nodes: %v", arch.Tag, err)
		return
	}

	var grafted []*Node
	childIDs := make([]string, 0, len(children))
	for _, c := range children {
		childIDs = append(childIDs, c.ID)
	}

	// Per-task execute, then per-task service. Both from the child's own params,
	// where computePlan put them.
	for _, child := range children {
		grafted = append(grafted, a.graftTaskExec(graph, arch, child, budget)...)
		grafted = append(grafted, a.graftTaskService(graph, arch, child, budget)...)
	}
	// Top-level services: architect-declared and not tied to a task.
	grafted = append(grafted, a.graftTopLevelServices(graph, arch, cr.Services, grafted, budget)...)
	// Checks last, waiting on everything above.
	grafted = append(grafted, a.graftValidation(graph, arch, cr.Validation, grafted, budget)...)

	if len(grafted) == 0 {
		return
	}
	newIDs := make([]string, 0, len(grafted))
	for _, n := range grafted {
		newIDs = append(newIDs, n.ID)
	}
	// Whatever was waiting for the coders must also wait for what runs them. The
	// up-front graft pointed downstream steps at the coders, and those are now
	// resolved — so without this a later step is ready before the server starts.
	if waited := graph.AlsoWaitFor(childIDs, newIDs); len(waited) > 0 {
		log.Printf("[dag] %d downstream node(s) now also wait for the architect's run and check nodes", len(waited))
	}
	log.Printf("[dag] architect %s: all %d coders resolved → grafted %d run/check node(s)",
		arch.Tag, len(children), len(grafted))
}

/*
 * graftTaskExec grafts the one-shot command a task declared.
 * desc: Skipped when it is the same command the task declares as a service, which
 *       would start the same process twice.
 * return: the node, or nothing.
 */
func (a *Agent) graftTaskExec(graph *Graph, arch, child *Node, budget *Budget) []*Node {
	execCmd, _ := child.Params["execute"].(string)
	if execCmd == "" {
		return nil
	}
	svcCmd := ""
	if svc, ok := child.Params["service"].(map[string]any); ok {
		svcCmd, _ = svc["command"].(string)
	}
	if execCmd == svcCmd {
		log.Printf("[dag] skipping execute node for %s — same command declared as service", child.Tag)
		return nil
	}
	if !budget.TrySpawnNode("bash", false) {
		return nil
	}
	// This is the step ComputeTimeout is about: the code a coder just wrote, being
	// run. It set no timeout, so bash's own 60s applied and an operator raising
	// tools.compute.timeout_sec got no change and no warning. Left unset when the
	// setting is, so bash's default still applies rather than a zero being passed
	// as "no time at all".
	params := map[string]any{"command": execCmd}
	if secs := int(a.cfg.ComputeTimeout.Seconds()); secs > 0 {
		params["timeout_sec"] = secs
	}
	n := &Node{
		Type:      NodeTool,
		ToolName:  "bash",
		Params:    params,
		SpawnedBy: arch.ID,
		Tag:       child.Tag + "_exec",
		Source:    "builtin",
	}
	id := graph.AddNode(n)
	graph.AddChild(arch.ID, id)
	a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: id, Node: graph.SnapshotNode(id)})
	log.Printf("[dag] architect run → grafted execute node %s: %s", id, execCmd)
	return []*Node{n}
}

/*
 * graftTaskService grafts the long-running process a task declared.
 * return: the node, or nothing.
 */
func (a *Agent) graftTaskService(graph *Graph, arch, child *Node, budget *Budget) []*Node {
	svc, ok := child.Params["service"].(map[string]any)
	if !ok {
		return nil
	}
	cmd, _ := svc["command"].(string)
	if cmd == "" {
		return nil
	}
	name, _ := svc["name"].(string)
	if name == "" {
		name = child.Tag + "_svc"
	}
	if !budget.TrySpawnNode("service", false) {
		return nil
	}
	params := map[string]any{"action": "start", "command": cmd, "name": name}
	if wd, _ := svc["workdir"].(string); wd != "" {
		params["workdir"] = wd
	}
	if p, ok := svc["port"].(float64); ok && p > 0 {
		params["port"] = p
	}
	n := &Node{
		Type:      NodeTool,
		ToolName:  "service",
		Params:    params,
		SpawnedBy: arch.ID,
		Tag:       name,
		Source:    "builtin",
	}
	id := graph.AddNode(n)
	graph.AddChild(arch.ID, id)
	a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: id, Node: graph.SnapshotNode(id)})
	log.Printf("[dag] architect run → grafted service node %s: %s", id, cmd)
	return []*Node{n}
}

/*
 * graftTopLevelServices grafts the architect's own services.
 * desc: Infrastructure rather than a task's own process, so these are grafted
 *       outside the node budget, as they were before.
 * param: already - what has been grafted so far, to avoid a duplicate by name.
 * return: the nodes grafted.
 */
func (a *Agent) graftTopLevelServices(graph *Graph, arch *Node, services []architectService, already []*Node, budget *Budget) []*Node {
	var out []*Node
	for _, svc := range services {
		if svc.Command == "" {
			continue
		}
		name := svc.Name
		if name == "" {
			name = "service"
		}
		dup := false
		for _, g := range already {
			if g.ToolName == "service" && g.Tag == name {
				dup = true
				break
			}
		}
		if dup {
			log.Printf("[dag] skipping top-level service %s — already grafted from task", name)
			continue
		}
		if !budget.TrySpawnNode("service", false) {
			log.Printf("[dag] budget exhausted, skipping remaining top-level services")
			break
		}
		params := map[string]any{"action": "start", "command": svc.Command, "name": name}
		if svc.Workdir != "" {
			params["workdir"] = svc.Workdir
		}
		if svc.Port > 0 {
			params["port"] = float64(svc.Port)
		}
		n := &Node{
			Type:      NodeTool,
			ToolName:  "service",
			Params:    params,
			SpawnedBy: arch.ID,
			Tag:       name,
			Source:    "builtin",
		}
		id := graph.AddNode(n)
		graph.AddChild(arch.ID, id)
		out = append(out, n)
		a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: id, Node: graph.SnapshotNode(id)})
		log.Printf("[dag] architect run → grafted top-level service node %s: %s", id, svc.Command)
	}
	return out
}

/*
 * graftValidation grafts the architect's checks, waiting on everything that runs.
 * desc: The validators are also stored on the graph, for replay after an
 *       investigation.
 * param: runNodes - the run nodes these checks must follow.
 * return: the nodes grafted.
 */
func (a *Agent) graftValidation(graph *Graph, arch *Node, checks []architectCheck, runNodes []*Node, budget *Budget) []*Node {
	if len(checks) == 0 {
		log.Printf("[dag] architect %s emitted no validation — no validation batch grafted", arch.Tag)
		return nil
	}
	for _, v := range checks {
		if v.Check != "" {
			graph.Validators = append(graph.Validators, ValidatorDef{Name: v.Name, Check: v.Check})
		}
	}
	var prior []string
	for _, n := range runNodes {
		prior = append(prior, n.ID)
	}
	var out []*Node
	for _, v := range checks {
		if v.Check == "" {
			continue
		}
		if !budget.TrySpawnNode("bash", false) {
			log.Printf("[dag] budget exhausted, skipping remaining validation checks")
			break
		}
		tag := "verify_" + sanitizeTag(v.Name)
		if tag == "verify_" {
			tag = fmt.Sprintf("verify_%d", len(out))
		}
		n := &Node{
			Type:      NodeTool,
			ToolName:  "bash",
			Params:    map[string]any{"command": "sleep 3 && " + v.Check, "timeout_sec": 20},
			DependsOn: append([]string{}, prior...),
			SpawnedBy: arch.ID,
			Tag:       tag,
			Source:    "builtin",
		}
		id := graph.AddNode(n)
		graph.AddChild(arch.ID, id)
		out = append(out, n)
		a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: id, Node: graph.SnapshotNode(id)})
	}
	if len(out) > 0 {
		log.Printf("[dag] architect run → grafted %d validation check(s)", len(out))
	}
	return out
}

// Ordering between the architect's tasks, with the edges that cannot be honoured
// named rather than dropped in silence.
//
// depends_on_tasks is an index into the task list, and three kinds of nonsense
// arrive in it. A task naming itself wires a node to its own ID: ReadyNodes needs
// every dependency terminal, so it never runs, and never fails either. Two tasks
// naming each other do the same to both. An index past the end of the list is a
// reference to a task that was never planned.
//
// None of those can fail the architect. Its coders are already grafted and the
// work is sound — what is wrong is only the order it asked for, and a plan that
// still runs in the wrong order beats a plan that does not run. So the edge is
// dropped and said out loud, because a stalled node leaves nothing to read.
//
// A stall is worse than it was. graftArchitectRunNodes waits for every coder to
// reach a terminal state, so one node waiting on itself now means the execute,
// service and check nodes are never grafted at all.

// taskDepEdge is one honoured ordering edge: task From waits for task To.
type taskDepEdge struct{ From, To int }

/*
 * resolveTaskDeps works out which ordering edges can be honoured.
 * desc: Edges are taken in the order given, and one is kept only when the task it
 *       points at cannot already reach the task it points from. That drops the edge
 *       that closes a cycle rather than the whole cycle, so as much of the
 *       architect's intended order survives as can.
 * param: deps - deps[i] is task i's depends_on_tasks.
 * param: live - live[i] reports whether task i actually got a node; an edge to a
 *        task the budget cut cannot be wired.
 * return: the edges to apply, and one sentence per edge that was not.
 */
func resolveTaskDeps(deps [][]int, live []bool) (edges []taskDepEdge, dropped []string) {
	n := len(deps)
	follows := make([][]bool, n) // follows[a][b]: a waits for b, transitively
	for i := range follows {
		follows[i] = make([]bool, n)
	}
	reaches := func(from, to int) bool {
		seen := make([]bool, n)
		stack := []int{from}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if cur == to {
				return true
			}
			if seen[cur] {
				continue
			}
			seen[cur] = true
			for next := 0; next < n; next++ {
				if follows[cur][next] {
					stack = append(stack, next)
				}
			}
		}
		return false
	}

	for i := 0; i < n; i++ {
		if i >= len(live) || !live[i] {
			continue
		}
		for _, j := range deps[i] {
			switch {
			case j == i:
				dropped = append(dropped, fmt.Sprintf("task %d depends on itself", i))
			case j < 0 || j >= n:
				dropped = append(dropped, fmt.Sprintf("task %d waits for task %d, which this plan does not have", i, j))
			case j >= len(live) || !live[j]:
				dropped = append(dropped, fmt.Sprintf("task %d waits for task %d, which has no node", i, j))
			case reaches(j, i):
				dropped = append(dropped, fmt.Sprintf("task %d waits for task %d, which already waits for task %d", i, j, i))
			default:
				follows[i][j] = true
				edges = append(edges, taskDepEdge{From: i, To: j})
			}
		}
	}
	return edges, dropped
}
