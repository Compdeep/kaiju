package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/user/kaiju/internal/agent/gates"
	"github.com/user/kaiju/internal/agent/llm"
	"github.com/user/kaiju/internal/agent/tools"
)

/*
 * observerOutput is the structured response from an observer call.
 * desc: Contains the observer's action decision, reason, optional new nodes
 *       to inject, and optional tags to cancel.
 */
type observerOutput struct {
	Action string     `json:"action"` // "continue", "inject", "cancel", "reflect"
	Reason string     `json:"reason"`
	Nodes  []PlanStep `json:"nodes"`    // set when action == "inject"
	Cancel []string   `json:"cancel"`   // tags/IDs to cancel when action == "cancel"
}

const defaultObserverRolePrompt = `You are an observer monitoring a live investigation.
A step just completed. Decide if the investigation should adapt.

Output JSON:
{
  "action": "continue|inject|cancel|reflect",
  "reason": "brief explanation",
  "nodes": [{"tool":"...","params":{},"depends_on":[],"tag":"..."}],
  "cancel": ["tag1", "tag2"]
}

Actions:
- "continue": result is expected, no changes needed. This is the most common response.
- "inject": result reveals something urgent — add new investigation steps immediately
- "cancel": result makes some pending steps pointless — cancel them by tag
- "reflect": enough evidence has accumulated — trigger a full reflection checkpoint

Rules:
- Default to "continue" unless the result is surprising or reveals new leads
- Only "inject" for genuinely new information that wasn't anticipated by the plan
- Only "cancel" if pending steps are provably pointless (e.g. target IP is already known-clean)
- Use "reflect" sparingly — only when enough evidence warrants a full review
- Output ONLY the JSON, no commentary`

/*
 * fireObserver runs a lightweight LLM call to evaluate a completed node's
 * result and decide if the investigation needs to adapt.
 * desc: Builds compact context (completed node result, pending steps, intent,
 *       original request, available tools), sends to the executor LLM, and
 *       returns the observer's decision on the completion channel.
 * param: ctx - context for the LLM call.
 * param: completedNode - the node whose result is being evaluated.
 * param: graph - the investigation graph.
 * param: budget - the execution budget.
 * param: ch - channel to send the observer's completion result.
 * param: trigger - the investigation trigger.
 * param: intent - optional IBE intent level(s).
 */
func (a *Agent) fireObserver(ctx context.Context, completedNode *Node,
	graph *Graph, budget *Budget, ch chan<- nodeCompletion, trigger Trigger, intent ...gates.Intent) {

	resolvedIntent := gates.IntentTell
	if len(intent) > 0 {
		resolvedIntent = intent[0]
	}

	// Build compact context for the observer
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Completed Node\nTag: %s\nTool: %s\nResult:\n%s\n",
		completedNode.Tag, completedNode.ToolName,
		Text.TruncateLog(completedNode.Result, 2000)))

	sb.WriteString(fmt.Sprintf("\n## Intent Level: %s\n", resolvedIntent))
	sb.WriteString(fmt.Sprintf("\n## Original Request\n\n%s\n", formatTrigger(trigger)))

	sb.WriteString("\n## Pending Steps\n")
	pending := graph.PendingNodes()
	if len(pending) == 0 {
		sb.WriteString("(none)\n")
	} else {
		for _, p := range pending {
			label := p.Tag
			if label == "" {
				label = p.ToolName
			}
			if label == "" {
				label = p.ID
			}
			if len(p.Params) > 0 {
				paramJSON, _ := json.Marshal(p.Params)
				sb.WriteString(fmt.Sprintf("- **%s** → `%s(%s)`\n", label, p.ToolName, string(paramJSON)))
			} else {
				sb.WriteString(fmt.Sprintf("- **%s** → `%s()`\n", label, p.ToolName))
			}
		}
	}

	// Create a graph node for tracking
	obsNode := &Node{
		Type: NodeObserver,
		Tag:  "observe_" + completedNode.Tag,
	}
	obsID := graph.AddNode(obsNode)
	graph.SetState(obsID, StateRunning)

	// Build system prompt with intent-filtered tools for injection
	var toolSection strings.Builder
	toolSection.WriteString("\n## Available Tools (for injection)\n")
	toolSection.WriteString(fmt.Sprintf("Only tools with impact ≤ %d (%s) will succeed.\n\n", int(resolvedIntent), resolvedIntent))
	for _, name := range a.registry.List() {
		skill, ok := a.registry.Get(name)
		if !ok {
			continue
		}
		impact := tools.GetImpact(skill, nil)
		if impact > int(resolvedIntent) {
			continue
		}
		toolSection.WriteString(fmt.Sprintf("- **%s**: %s — `%s`\n", name, skill.Description(), string(skill.Parameters())))
	}

	messages := []llm.Message{
		{Role: "system", Content: ComposeSystemPrompt(a.soulPrompt, defaultObserverRolePrompt+toolSection.String()+a.fleetSection())},
		{Role: "user", Content: sb.String()},
	}

	resp, err := a.executor.Complete(ctx, &llm.ChatRequest{
		Messages:    messages,
		Temperature: a.cfg.Temperature,
		MaxTokens:   1024,
	})
	if err != nil {
		log.Printf("[dag] observer failed for %s: %v", completedNode.Tag, err)
		graph.SetResult(obsID, "observer error: "+err.Error())
		ch <- nodeCompletion{NodeID: obsID, Result: ""}
		return
	}

	if len(resp.Choices) == 0 {
		graph.SetResult(obsID, "no response")
		ch <- nodeCompletion{NodeID: obsID, Result: ""}
		return
	}

	raw := resp.Choices[0].Message.Content
	log.Printf("[dag] observer for %s: %s", completedNode.Tag, Text.TruncateLog(raw, 150))
	graph.SetResult(obsID, raw)

	ch <- nodeCompletion{NodeID: obsID, Result: raw}
}

/*
 * parseObserverOutput extracts the observer's decision from LLM output.
 * desc: Strips code fences, parses JSON, and validates the action field.
 *       Returns a default "continue" if the input is empty.
 * param: raw - the raw LLM output string.
 * return: parsed observerOutput pointer, or error if JSON is invalid.
 */
func parseObserverOutput(raw string) (*observerOutput, error) {
	cleaned := Text.StripCodeFence(raw)
	if cleaned == "" {
		return &observerOutput{Action: "continue"}, nil
	}

	var output observerOutput
	if err := json.Unmarshal([]byte(cleaned), &output); err != nil {
		return nil, fmt.Errorf("invalid observer JSON: %w", err)
	}

	switch output.Action {
	case "continue", "inject", "cancel", "reflect":
		// valid
	default:
		return nil, fmt.Errorf("unknown observer action: %q", output.Action)
	}

	return &output, nil
}
