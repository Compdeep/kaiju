package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/user/kaiju/internal/agent/llm"
)

/*
 * microPlannerOutput is the LLM's response to a node failure.
 * desc: Contains the recovery action (retry, skip, replace) and optional
 *       replacement plan steps.
 */
type microPlannerOutput struct {
	Action string     `json:"action"` // "retry", "skip", "replace"
	Nodes  []PlanStep `json:"nodes"`  // replacement/retry steps (empty for skip)
}

/*
 * fireMicroPlanner runs a scoped repair LLM call for a failed node.
 * desc: Builds a prompt with the failed step's details (error, params, schema),
 *       sibling results for context, and available tools for replacement.
 *       Sends the result back on the completion channel as a nodeCompletion
 *       for the micro-planner node itself.
 * param: ctx - context for the LLM call.
 * param: mpNode - the micro-planner node in the graph.
 * param: failedNode - the node that failed and needs repair.
 * param: graph - the investigation graph.
 * param: budget - the execution budget.
 * param: ch - channel to send the micro-planner's completion result.
 */
func (a *Agent) fireMicroPlanner(ctx context.Context, mpNode *Node, failedNode *Node,
	graph *Graph, budget *Budget, ch chan<- nodeCompletion, trigger ...Trigger) {

	// List available tools for "replace" option
	var toolList string
	for _, name := range a.registry.List() {
		if sk, ok := a.registry.Get(name); ok {
			toolList += fmt.Sprintf("- %s: %s\n", name, sk.Description())
		}
	}

	var prompt string

	if failedNode.Type == NodeCompute {
		// Enhanced context for compute node failures — show goal, not raw params
		prompt = fmt.Sprintf("A compute step failed.\n\nGoal: %v\nMode: %v\nError: %v\n",
			failedNode.Params["goal"], failedNode.Params["mode"], failedNode.Error)
		if blueprintRef, ok := failedNode.Params["blueprint_ref"].(string); ok {
			prompt += fmt.Sprintf("Plan reference: %s\n", blueprintRef)
		}
		if hints, ok := failedNode.Params["hints"].([]any); ok && len(hints) > 0 {
			prompt += "Previous attempts:\n"
			for i, h := range hints {
				prompt += fmt.Sprintf("  %d. %v\n", i+1, h)
			}
		}
		prompt += fmt.Sprintf("\nAvailable tools for replacement:\n%s\nResults from other steps at the same stage:\n", toolList)
	} else {
		// Standard prompt for tool nodes — show params without hints to avoid bloat
		paramSchema := ""
		if sk, ok := a.registry.Get(failedNode.ToolName); ok {
			paramSchema = fmt.Sprintf("\nTool %q parameters: %s\n", failedNode.ToolName, string(sk.Parameters()))
		}
		cleanParams := make(map[string]any)
		for k, v := range failedNode.Params {
			if k != "hints" {
				cleanParams[k] = v
			}
		}
		prompt = fmt.Sprintf("A step failed during the investigation.\n\nFailed step: %s (tool: %s)\nError: %v\nParameters: %v\n%s\n",
			failedNode.Tag, failedNode.ToolName, failedNode.Error, cleanParams, paramSchema)
		if hints, ok := failedNode.Params["hints"].([]any); ok && len(hints) > 0 {
			prompt += "Previous attempts that also failed:\n"
			for i, h := range hints {
				prompt += fmt.Sprintf("  %d. %v\n", i+1, h)
			}
			prompt += "Try a DIFFERENT approach.\n"
		}
		prompt += fmt.Sprintf("\nAvailable tools for replacement:\n%s\nResults from other steps at the same stage:\n", toolList)
	}

	siblings := graph.SiblingResults(failedNode.ID)
	if len(siblings) == 0 {
		prompt += "(none)\n"
	} else {
		for label, result := range siblings {
			prompt += fmt.Sprintf("- %s: %s\n", label, Text.TruncateLog(result, 500))
		}
	}

	// Include user context (truncated to avoid token burn on retries)
	if len(trigger) > 0 {
		query := formatTrigger(trigger[0])
		if query != "" {
			prompt += fmt.Sprintf("\nOriginal request: %s\n", Text.TruncateLog(query, 200))
		}
		// Include all user messages for context (truncated to keep prompt reasonable)
		var userMsgs []llm.Message
		for _, m := range trigger[0].History {
			if m.Role == "user" {
				userMsgs = append(userMsgs, m)
			}
		}
		for _, m := range userMsgs {
			prompt += fmt.Sprintf("User context: %s\n", Text.TruncateLog(m.Content, 100))
		}
		log.Printf("[dag] micro-planner context: query=%q, history=%d user msgs (showing all)",
			Text.TruncateLog(query, 80), len(userMsgs))
		for i, m := range userMsgs {
			log.Printf("[dag] micro-planner user msg %d: %s", i, Text.TruncateLog(m.Content, 100))
		}
	} else {
		log.Printf("[dag] micro-planner: no trigger context available")
	}

	// Log hints count
	if hints, ok := failedNode.Params["hints"].([]any); ok {
		log.Printf("[dag] micro-planner hints: %d accumulated failures for %s", len(hints), failedNode.Tag)
	}

	prompt += `
Decide how to recover. Output JSON:
{
  "action": "retry|skip|replace",
  "nodes": [{"tool": "...", "params": {...}, "depends_on": [], "tag": "..."}]
}

- "skip": abandon this node, let dependents proceed without its data. nodes=[]
- "retry": try the same tool with CORRECT parameters (see schema above). nodes=[one entry]
- "replace": use alternative tool(s) from the available list. nodes=[one or more]
- If the same tool already failed, prefer "skip" or "replace" over another retry.
- NEVER use "compute" as a replacement tool. Compute is for code generation, not repairs. Use bash, file_read, file_write, net_info, web_fetch, or service instead.

Output ONLY the JSON, no commentary.`

	log.Printf("[dag] micro-planner prompt for %s (%d bytes): %s", failedNode.Tag, len(prompt), Text.TruncateLog(prompt, 800))

	messages := []llm.Message{
		{Role: "system", Content: defaultMicroPlannerRolePrompt},
		{Role: "user", Content: prompt},
	}

	// Escalate to reasoning model after 3 failures — executor model is stuck
	client := a.executor
	modelLabel := "executor"
	if hints, ok := failedNode.Params["hints"].([]any); ok && len(hints) >= 3 {
		client = a.llm
		modelLabel = "reasoning"
		log.Printf("[dag] micro-planner escalating to reasoning model after %d failures on %s", len(hints), failedNode.Tag)
		a.broadcastDAGEvent(DAGEvent{Type: "escalation", NodeID: mpNode.ID, Node: &NodeInfo{
			ID: mpNode.ID, Type: "micro_planner", Tag: fmt.Sprintf("escalated after %d failures", len(hints)),
		}})
	}

	log.Printf("[dag] micro-planner calling %s model for %s", modelLabel, failedNode.Tag)

	resp, err := client.Complete(ctx, &llm.ChatRequest{
		Messages:    messages,
		Temperature: a.cfg.Temperature,
		MaxTokens:   1024,
	})
	if err != nil {
		ch <- nodeCompletion{NodeID: mpNode.ID, Err: fmt.Errorf("micro-planner LLM (%s): %w", modelLabel, err)}
		return
	}

	if len(resp.Choices) == 0 {
		ch <- nodeCompletion{NodeID: mpNode.ID, Err: fmt.Errorf("micro-planner: no choices")}
		return
	}

	raw := resp.Choices[0].Message.Content
	log.Printf("[dag] micro-planner output: %s", Text.TruncateLog(raw, 200))

	ch <- nodeCompletion{NodeID: mpNode.ID, Result: raw}
}

/*
 * parseMicroPlannerOutput interprets the micro-planner's response and
 * creates replacement nodes in the graph.
 * desc: Parses the JSON response, then handles each action type: skip marks
 *       the failed node as skipped, retry creates a single replacement node
 *       with the same deps, replace creates one or more alternative nodes.
 *       Dependents of the failed node are rewritten to point to replacements.
 * param: raw - the raw LLM output string.
 * param: failedNode - the original failed node.
 * param: graph - the investigation graph.
 * param: budget - the execution budget.
 * return: slice of created replacement Node pointers, or error.
 */
func parseMicroPlannerOutput(raw string, failedNode *Node, graph *Graph, budget *Budget) ([]*Node, error) {
	cleaned := Text.StripCodeFence(raw)

	var output microPlannerOutput
	if err := json.Unmarshal([]byte(cleaned), &output); err != nil {
		return nil, fmt.Errorf("invalid micro-planner JSON: %w", err)
	}

	switch output.Action {
	case "skip":
		// Mark failed node as skipped so dependents can proceed
		graph.SetState(failedNode.ID, StateSkipped)
		log.Printf("[dag] micro-planner: skipping failed node %s", failedNode.ID)
		return nil, nil

	case "retry":
		if len(output.Nodes) == 0 {
			return nil, fmt.Errorf("retry action with no nodes")
		}
		step := output.Nodes[0]
		if !budget.TrySpawnNode(step.Tool, false) {
			return nil, fmt.Errorf("budget exhausted for retry")
		}
		nodeType := NodeTool
		if step.Tool == "compute" {
			nodeType = NodeCompute
		}
		n := &Node{
			Type:      nodeType,
			ToolName: step.Tool,
			Params:    step.Params,
			DependsOn: failedNode.DependsOn, // inherit parent's deps
			SpawnedBy: failedNode.ID,
			Tag:       step.Tag,
		}
		if n.Tag == "" {
			n.Tag = failedNode.Tag + "_retry"
		}
		propagateRetryState(n, failedNode)
		graph.AddNode(n)
		// Rewrite dependents of the failed node to depend on the retry instead
		rewriteDependents(graph, failedNode.ID, n.ID)
		return []*Node{n}, nil

	case "replace":
		if len(output.Nodes) == 0 {
			return nil, fmt.Errorf("replace action with no nodes")
		}
		var created []*Node
		for _, step := range output.Nodes {
			if !budget.TrySpawnNode(step.Tool, false) {
				log.Printf("[dag] budget exhausted during replace, created %d of %d",
					len(created), len(output.Nodes))
				break
			}
			nodeType := NodeTool
			if step.Tool == "compute" {
				nodeType = NodeCompute
			}
			n := &Node{
				Type:      nodeType,
				ToolName: step.Tool,
				Params:    step.Params,
				DependsOn: failedNode.DependsOn,
				SpawnedBy: failedNode.ID,
				Tag:       step.Tag,
			}
			propagateRetryState(n, failedNode)
			graph.AddNode(n)
			created = append(created, n)
		}
		// Rewrite dependents to depend on all replacement nodes
		if len(created) > 0 {
			rewriteDependentsMulti(graph, failedNode.ID, created)
		}
		return created, nil

	default:
		return nil, fmt.Errorf("unknown micro-planner action: %q", output.Action)
	}
}

/*
 * rewriteDependents changes all nodes that depended on oldID to depend on newID instead.
 * desc: Scans all pending nodes and replaces oldID in their DependsOn slices.
 *       Acquires the graph write lock.
 * param: graph - the investigation graph.
 * param: oldID - the failed node's ID to replace.
 * param: newID - the replacement node's ID.
 */
func rewriteDependents(graph *Graph, oldID, newID string) {
	graph.mu.Lock()
	defer graph.mu.Unlock()

	for _, n := range graph.nodes {
		if n.State != StatePending {
			continue
		}
		for i, dep := range n.DependsOn {
			if dep == oldID {
				n.DependsOn[i] = newID
			}
		}
	}
}

/*
 * rewriteDependentsMulti changes all nodes that depended on oldID to depend
 * on all replacement node IDs instead.
 * desc: Scans all pending nodes and expands oldID references into the full
 *       set of replacement IDs. Acquires the graph write lock.
 * param: graph - the investigation graph.
 * param: oldID - the failed node's ID to replace.
 * param: replacements - slice of replacement nodes whose IDs will be used.
 */
func rewriteDependentsMulti(graph *Graph, oldID string, replacements []*Node) {
	graph.mu.Lock()
	defer graph.mu.Unlock()

	replIDs := make([]string, len(replacements))
	for i, r := range replacements {
		replIDs[i] = r.ID
	}

	for _, n := range graph.nodes {
		if n.State != StatePending {
			continue
		}
		for i, dep := range n.DependsOn {
			if dep == oldID {
				// Replace oldID with all replacement IDs
				newDeps := make([]string, 0, len(n.DependsOn)-1+len(replIDs))
				newDeps = append(newDeps, n.DependsOn[:i]...)
				newDeps = append(newDeps, replIDs...)
				newDeps = append(newDeps, n.DependsOn[i+1:]...)
				n.DependsOn = newDeps
				break // only one occurrence of oldID per dep list
			}
		}
	}
}

/*
 * rewriteDependentsExcluding changes deps from oldID to newID, skipping excludeID.
 * desc: Used by compute plan grafting — the follow-up node depends on the plan
 *       node and must not be rewritten to depend on itself.
 */
func rewriteDependentsExcluding(graph *Graph, oldID, newID, excludeID string) {
	graph.mu.Lock()
	defer graph.mu.Unlock()

	for _, n := range graph.nodes {
		if n.State != StatePending || n.ID == excludeID {
			continue
		}
		for i, dep := range n.DependsOn {
			if dep == oldID {
				n.DependsOn[i] = newID
			}
		}
	}
}

/*
 * rewriteDependentsMultiExcluding replaces oldID with all replacement node IDs,
 * skipping the replacement nodes themselves.
 * desc: Used when compute plan grafts multiple child nodes. Downstream nodes that
 *       depended on the plan node now depend on ALL children (must wait for all
 *       work items to complete). The children themselves are excluded because they
 *       correctly depend on the plan node for the blueprint_ref.
 */
func rewriteDependentsMultiExcluding(graph *Graph, oldID string, replacements []*Node) {
	graph.mu.Lock()
	defer graph.mu.Unlock()

	// Build ID sets
	replIDs := make([]string, len(replacements))
	excludeSet := make(map[string]bool, len(replacements))
	for i, r := range replacements {
		replIDs[i] = r.ID
		excludeSet[r.ID] = true
	}

	for _, n := range graph.nodes {
		if n.State != StatePending || excludeSet[n.ID] {
			continue
		}
		for i, dep := range n.DependsOn {
			if dep == oldID {
				newDeps := make([]string, 0, len(n.DependsOn)-1+len(replIDs))
				newDeps = append(newDeps, n.DependsOn[:i]...)
				newDeps = append(newDeps, replIDs...)
				newDeps = append(newDeps, n.DependsOn[i+1:]...)
				n.DependsOn = newDeps
				break
			}
		}
	}
}

/*
 * propagateComputeState preserves NodeCompute type and carries forward
 * hints, blueprint_ref, query, goal, mode, and context when retrying compute nodes.
 */
/*
 * propagateRetryState accumulates error hints across retries for ALL node types.
 * desc: Every retry gets the full history of what failed before so the
 *       microplanner and tools can try different approaches.
 */
func propagateRetryState(replacement *Node, failed *Node) {
	if replacement.Params == nil {
		replacement.Params = make(map[string]any)
	}

	// Accumulate hints — only the short error summary, not full params/nested hints
	var hints []any
	if prev, ok := failed.Params["hints"].([]any); ok {
		hints = append(hints, prev...)
	}
	if failed.Error != nil {
		// Extract just the key error info, not the full nested params
		errStr := failed.Error.Error()
		// If it's a bash_error JSON, extract just command + stderr
		var bashErr struct {
			Command string `json:"command"`
			Stderr  string `json:"stderr"`
			Error   string `json:"error"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(errStr, "bash failed: ")), &bashErr) == nil && bashErr.Command != "" {
			errStr = fmt.Sprintf("%s: %s", bashErr.Command, strings.TrimSpace(bashErr.Stderr))
		}
		hint := Text.TruncateLog(errStr, 150)
		hints = append(hints, hint)
	}
	if len(hints) > 0 {
		replacement.Params["hints"] = hints
	}

	// Compute-specific: preserve type and carry forward essential params
	if failed.Type == NodeCompute {
		replacement.Type = NodeCompute
		for _, key := range []string{"blueprint_ref", "query", "goal", "mode", "context", "task_files", "brief", "structure", "interfaces", "execute", "service"} {
			if v, ok := failed.Params[key]; ok {
				if _, exists := replacement.Params[key]; !exists {
					replacement.Params[key] = v
				}
			}
		}
	}
}
