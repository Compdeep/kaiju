package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

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
	graph *Graph, budget *Budget, ch chan<- nodeCompletion) {

	// Include the failed tool's parameter schema so the LLM knows correct params
	paramSchema := ""
	if sk, ok := a.registry.Get(failedNode.ToolName); ok {
		paramSchema = fmt.Sprintf("\nTool %q parameters: %s\n", failedNode.ToolName, string(sk.Parameters()))
	}

	// List available tools for "replace" option
	var toolList string
	for _, name := range a.registry.List() {
		if sk, ok := a.registry.Get(name); ok {
			toolList += fmt.Sprintf("- %s: %s\n", name, sk.Description())
		}
	}

	prompt := fmt.Sprintf(`A step failed during the investigation.

Failed step: %s (tool: %s)
Error: %v
Parameters: %v
%s
Available tools for replacement:
%s
Results from other steps at the same stage:
`, failedNode.Tag, failedNode.ToolName, failedNode.Error, failedNode.Params, paramSchema, toolList)

	siblings := graph.SiblingResults(failedNode.ID)
	if len(siblings) == 0 {
		prompt += "(none)\n"
	} else {
		for label, result := range siblings {
			prompt += fmt.Sprintf("- %s: %s\n", label, Text.TruncateLog(result, 500))
		}
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

Output ONLY the JSON, no commentary.`

	messages := []llm.Message{
		{Role: "system", Content: ComposeSystemPrompt(a.soulPrompt, defaultMicroPlannerRolePrompt)},
		{Role: "user", Content: prompt},
	}

	resp, err := a.executor.Complete(ctx, &llm.ChatRequest{
		Messages:    messages,
		Temperature: a.cfg.Temperature,
		MaxTokens:   1024,
	})
	if err != nil {
		ch <- nodeCompletion{NodeID: mpNode.ID, Err: fmt.Errorf("micro-planner LLM: %w", err)}
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
		n := &Node{
			Type:      NodeSkill,
			ToolName: step.Tool,
			Params:    step.Params,
			DependsOn: failedNode.DependsOn, // inherit parent's deps
			SpawnedBy: failedNode.ID,
			Tag:       step.Tag,
		}
		if n.Tag == "" {
			n.Tag = failedNode.Tag + "_retry"
		}
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
			n := &Node{
				Type:      NodeSkill,
				ToolName: step.Tool,
				Params:    step.Params,
				DependsOn: failedNode.DependsOn,
				SpawnedBy: failedNode.ID,
				Tag:       step.Tag,
			}
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
