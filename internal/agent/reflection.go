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
 * reflectionOutput is the structured response from a reflection checkpoint.
 * desc: Contains the reflection decision (continue/conclude/replan), optional
 *       verdict text, optional replacement plan steps, and a reason.
 */
type reflectionOutput struct {
	Decision   string          `json:"decision"`  // "continue", "conclude", "replan"
	RawVerdict json.RawMessage `json:"verdict"`   // set when decision == "conclude" — may be string or object
	Verdict    string          `json:"-"`          // parsed from RawVerdict
	Nodes      []PlanStep      `json:"nodes"`     // set when decision == "replan"
	Reason     string          `json:"reason"`
	Aggregate  *bool           `json:"aggregate,omitempty"` // set when decision == "conclude" — reflector decides if aggregator needed
}

/*
 * fireReflection runs a reflection checkpoint LLM call.
 * desc: Reviews evidence gathered so far, pending steps, executed tool calls,
 *       capability gaps, and available tools, then decides whether to continue,
 *       conclude early, or replan the remaining investigation.
 *       Intent is threaded through so the reflection knows what tools are permissible.
 * param: ctx - context for the LLM call.
 * param: rNode - the reflection node in the graph.
 * param: graph - the investigation graph.
 * param: budget - the execution budget.
 * param: ch - channel to send the reflection's completion result.
 * param: trigger - the investigation trigger.
 * param: intent - optional IBE intent level(s).
 */
func (a *Agent) fireReflection(ctx context.Context, rNode *Node, graph *Graph,
	budget *Budget, ch chan<- nodeCompletion, trigger Trigger, intent ...gates.Intent) {

	// Collect evidence so far
	evidence := graph.ResolvedResultsSoFar()

	var sb strings.Builder
	sb.WriteString("## Evidence Gathered So Far\n\n")
	if len(evidence) == 0 {
		sb.WriteString("(no evidence yet)\n")
	} else {
		for label, result := range evidence {
			sb.WriteString(fmt.Sprintf("**%s:**\n%s\n\n", label, result))
		}
	}

	// Show pending work with params so reflection can evaluate them
	pending := graph.PendingNodes()
	sb.WriteString("## Remaining Planned Steps\n\n")
	if len(pending) == 0 {
		sb.WriteString("(none — all steps complete)\n")
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

	// Show previously executed tool calls so reflection doesn't replan duplicates
	executed := graph.ExecutedTools()
	if len(executed) > 0 {
		sb.WriteString("## Previously Executed\n")
		sb.WriteString("Do not replan with these — the data is already in the evidence above.\n\n")
		for _, e := range executed {
			if len(e.Params) > 0 {
				paramJSON, _ := json.Marshal(e.Params)
				sb.WriteString(fmt.Sprintf("- `%s(%s)` → %d bytes\n", e.Tool, string(paramJSON), e.ResultSize))
			} else {
				sb.WriteString(fmt.Sprintf("- `%s()` → %d bytes\n", e.Tool, e.ResultSize))
			}
		}
		sb.WriteString("\n")
	}

	// Resolve intent for this reflection
	resolvedIntent := gates.IntentTell
	if len(intent) > 0 {
		resolvedIntent = intent[0]
	}

	sb.WriteString(fmt.Sprintf("\n## Intent Level: %s\n", resolvedIntent))
	sb.WriteString(fmt.Sprintf("\n## Original Request\n\n%s\n", formatTrigger(trigger)))

	// Inject declared capability gaps — reflection must not replan for these
	if len(graph.Gaps) > 0 {
		sb.WriteString("\n## Declared Capability Gaps\n")
		sb.WriteString("The following capabilities are NOT available. Do NOT replan to address these — they cannot be solved with current tools. Acknowledge them in your conclusion.\n\n")
		for _, gap := range graph.Gaps {
			sb.WriteString(fmt.Sprintf("- %s\n", gap))
		}
		sb.WriteString("\n")
	}

	// Include available tools filtered by intent so reflection only replans
	// with tools that will pass the gate
	var toolSection strings.Builder
	toolSection.WriteString("\n## Available Tools (for replanning)\n")
	toolSection.WriteString(fmt.Sprintf("Only tools with impact ≤ %d (%s) will succeed.\n\n", int(resolvedIntent), resolvedIntent))
	for _, name := range a.registry.List() {
		skill, ok := a.registry.Get(name)
		if !ok {
			continue
		}
		impact := tools.GetImpact(skill, nil)
		if impact > int(resolvedIntent) {
			continue // omit tools that would be blocked
		}
		toolSection.WriteString(fmt.Sprintf("- **%s**: %s — `%s`\n", name, skill.Description(), string(skill.Parameters())))
	}

	messages := []llm.Message{
		{
			Role:    "system",
			Content: ComposeSystemPrompt(a.soulPrompt, defaultReflectionRolePrompt+toolSection.String()+a.fleetSection()),
		},
		{Role: "user", Content: sb.String()},
	}

	resp, err := a.executor.Complete(ctx, &llm.ChatRequest{
		Messages:    messages,
		Temperature: a.cfg.Temperature,
		MaxTokens:   4096,
	})
	if err != nil {
		ch <- nodeCompletion{NodeID: rNode.ID, Err: fmt.Errorf("reflection LLM: %w", err)}
		return
	}

	if len(resp.Choices) == 0 {
		ch <- nodeCompletion{NodeID: rNode.ID, Err: fmt.Errorf("reflection: no choices")}
		return
	}

	raw := resp.Choices[0].Message.Content
	log.Printf("[dag] reflection output: %s", Text.TruncateLog(raw, 200))

	ch <- nodeCompletion{NodeID: rNode.ID, Result: raw}
}

const interjectionReflectionPrompt = `You are a reflection checkpoint in an active investigation.
The human operator has sent an urgent message that requires your attention.
Review their message alongside the evidence gathered so far, then decide the next action.

Output JSON:
{
  "decision": "continue|conclude|replan",
  "reason": "brief explanation of how you addressed the operator's message",
  "verdict": "final summary (only if decision=conclude)",
  "nodes": [{"tool":"...","params":{},"depends_on":[],"tag":"..."}] (only if decision=replan)
}

Rules:
- The operator's message is the PRIMARY input — address it directly
- "continue": operator's message is noted, current plan still makes sense
- "conclude": operator wants to stop, or evidence is now sufficient
- "replan": operator wants a different direction — provide new steps
- Output ONLY the JSON, no commentary`

/*
 * fireInterjectionReflection runs a reflection checkpoint triggered by a human message.
 * desc: Unlike fireReflection, the human message is the primary focus, not a
 *       side-channel. Builds context from the operator message, evidence, pending
 *       steps, intent, and available tools, then sends the decision on the
 *       completion channel.
 * param: ctx - context for the LLM call.
 * param: rNode - the interjection reflection node in the graph.
 * param: graph - the investigation graph.
 * param: budget - the execution budget.
 * param: ch - channel to send the reflection's completion result.
 * param: trigger - the investigation trigger.
 * param: humanMsg - the operator's message text.
 * param: intent - optional IBE intent level(s).
 */
func (a *Agent) fireInterjectionReflection(ctx context.Context, rNode *Node, graph *Graph,
	budget *Budget, ch chan<- nodeCompletion, trigger Trigger, humanMsg string, intent ...gates.Intent) {

	evidence := graph.ResolvedResultsSoFar()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Operator Message\n\n%s\n\n", humanMsg))

	sb.WriteString("## Evidence Gathered So Far\n\n")
	if len(evidence) == 0 {
		sb.WriteString("(no evidence yet)\n")
	} else {
		for label, result := range evidence {
			sb.WriteString(fmt.Sprintf("**%s:**\n%s\n\n", label, result))
		}
	}

	pending := graph.PendingNodes()
	sb.WriteString("## Remaining Planned Steps\n\n")
	if len(pending) == 0 {
		sb.WriteString("(none — all steps complete)\n")
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

	resolvedIntent := gates.IntentTell
	if len(intent) > 0 {
		resolvedIntent = intent[0]
	}

	sb.WriteString(fmt.Sprintf("\n## Intent Level: %s\n", resolvedIntent))
	sb.WriteString(fmt.Sprintf("\n## Original Request\n\n%s\n", formatTrigger(trigger)))

	// Include intent-filtered tools for replanning
	var toolSection strings.Builder
	toolSection.WriteString("\n## Available Tools (for replanning)\n")
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
		{
			Role:    "system",
			Content: ComposeSystemPrompt(a.soulPrompt, interjectionReflectionPrompt+toolSection.String()+a.fleetSection()),
		},
		{Role: "user", Content: sb.String()},
	}

	resp, err := a.executor.Complete(ctx, &llm.ChatRequest{
		Messages:    messages,
		Temperature: a.cfg.Temperature,
		MaxTokens:   4096,
	})
	if err != nil {
		ch <- nodeCompletion{NodeID: rNode.ID, Err: fmt.Errorf("interjection reflection LLM: %w", err)}
		return
	}

	if len(resp.Choices) == 0 {
		ch <- nodeCompletion{NodeID: rNode.ID, Err: fmt.Errorf("interjection reflection: no choices")}
		return
	}

	raw := resp.Choices[0].Message.Content
	log.Printf("[dag] interjection reflection output: %s", Text.TruncateLog(raw, 200))

	ch <- nodeCompletion{NodeID: rNode.ID, Result: raw}
}

/*
 * parseReflectionOutput extracts the reflection decision from LLM output.
 * desc: Strips code fences, parses JSON, normalizes the verdict field
 *       (which may be a string or object), and validates the decision.
 *       Falls back to using reason as verdict if decision is "conclude"
 *       but verdict is empty.
 * param: raw - the raw LLM output string.
 * return: parsed reflectionOutput pointer, or error if JSON is invalid.
 */
func parseReflectionOutput(raw string) (*reflectionOutput, error) {
	cleaned := Text.StripCodeFence(raw)

	var output reflectionOutput
	if err := json.Unmarshal([]byte(cleaned), &output); err != nil {
		return nil, fmt.Errorf("invalid reflection JSON: %w", err)
	}

	// Parse verdict — LLMs may return a string or a structured object
	if len(output.RawVerdict) > 0 {
		var s string
		if json.Unmarshal(output.RawVerdict, &s) == nil {
			output.Verdict = s
		} else {
			// Object or other type — stringify it
			output.Verdict = string(output.RawVerdict)
		}
	}

	switch output.Decision {
	case "continue", "conclude", "replan":
		// valid
	default:
		return nil, fmt.Errorf("unknown reflection decision: %q", output.Decision)
	}

	if output.Decision == "conclude" && output.Verdict == "" {
		output.Verdict = output.Reason // graceful fallback
	}

	return &output, nil
}
