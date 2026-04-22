package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/gates"
	"github.com/Compdeep/kaiju/internal/agent/llm"
)

/*
 * reflectionOutput is the structured response from a reflection checkpoint.
 * desc: Contains the reflection decision (continue/conclude/investigate),
 *       optional verdict text, and the problem statement passed to Holmes.
 */
type reflectionOutput struct {
	Decision   string          `json:"decision"`            // "continue", "conclude", "investigate"
	Progress   string          `json:"progress,omitempty"`  // "productive", "diminishing", "stuck" — scheduler-consumed; "" defaults to productive
	Summary    string          `json:"summary"`             // status description
	Problem    string          `json:"problem,omitempty"`   // only for investigate: what's wrong (passed to Holmes)
	RawVerdict json.RawMessage `json:"verdict"`             // only for conclude — may be string or object
	Verdict    string          `json:"-"`                   // parsed from RawVerdict
	Reason     string          `json:"reason"`              // backward compat — used as Summary fallback
	Aggregate  *bool           `json:"aggregate,omitempty"` // only for conclude
}

/*
 * fireReflection runs a reflection checkpoint LLM call.
 * desc: Reviews node returns, worklog, and previous debug attempts to decide
 *       continue / conclude / investigate. Context is built by the caller via
 *       ContextGate and passed in as a ContextResponse.
 * param: ctx - context for the LLM call.
 * param: rNode - the reflection node in the graph.
 * param: graph - the investigation graph.
 * param: budget - the execution budget.
 * param: ch - channel to send the reflection's completion result.
 * param: trigger - the investigation trigger.
 * param: gateCtx - assembled context from ContextGate.
 * param: intent - optional IGX intent level.
 */
func (a *Agent) fireReflection(ctx context.Context, rNode *Node, graph *Graph,
	budget *Budget, ch chan<- nodeCompletion, trigger Trigger, gateCtx *ContextResponse, intent ...gates.Intent) {

	sysPrompt := fmt.Sprintf(reflectorClassifierPrompt, a.FormatRule()) + a.fleetSection()
	userPrompt := assembleReflectorPrompt(graph, gateCtx, trigger)

	messages := []llm.Message{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userPrompt},
	}

	started := time.Now()
	resp, err := a.executor.Complete(ctx, &llm.ChatRequest{
		Messages:    messages,
		Tools:       []llm.ToolDef{reflectorToolDef()},
		ToolChoice:  "required",
		Temperature: a.cfg.Temperature,
		MaxTokens:   1024,
	})

	trace := LLMTrace{
		AlertID:   trigger.AlertID,
		NodeID:    rNode.ID,
		NodeType:  "reflector",
		Tag:       rNode.Tag,
		Model:     "executor",
		Started:   started,
		System:    sysPrompt,
		User:      userPrompt,
		LatencyMS: time.Since(started).Milliseconds(),
	}
	if gateCtx != nil {
		trace.GateReturned = gateCtx.Sources
	}

	if err != nil {
		trace.Err = err.Error()
		WriteLLMTrace(trace)
		ch <- nodeCompletion{NodeID: rNode.ID, Err: fmt.Errorf("reflection LLM: %w", err)}
		return
	}

	raw, err := extractToolArgs(resp)
	if err != nil {
		trace.Err = err.Error()
		WriteLLMTrace(trace)
		ch <- nodeCompletion{NodeID: rNode.ID, Err: fmt.Errorf("reflection: %w", err)}
		return
	}
	trace.Output = raw
	trace.TokensIn = resp.Usage.PromptTokens
	trace.TokensOut = resp.Usage.CompletionTokens
	WriteLLMTrace(trace)

	log.Printf("[dag] reflection output: %s", Text.TruncateLog(raw, 200))

	ch <- nodeCompletion{NodeID: rNode.ID, Result: raw, TokensIn: resp.Usage.PromptTokens, TokensOut: resp.Usage.CompletionTokens}
}

// assembleReflectorPrompt builds the reflector's user message from a gate
// response. Sections: Original Request, Graph Summary, FAILURES DETECTED,
// Evidence, Previous Debug Attempts.
func assembleReflectorPrompt(graph *Graph, gateCtx *ContextResponse, trigger Trigger) string {
	var sb strings.Builder

	sb.WriteString("## Original Request\n\n")
	sb.WriteString(formatTrigger(trigger))
	sb.WriteString("\n\n")

	// Graph summary — quick counts
	if graph != nil {
		failed := len(graph.FailedNodes())
		skipped := len(graph.SkippedNodes())
		pending := len(graph.PendingNodes())
		resolved := len(graph.ResolvedResultsSoFar())
		sb.WriteString("## Graph Summary\n\n")
		sb.WriteString(fmt.Sprintf("%d resolved, %d failed, %d skipped, %d pending\n", resolved, failed, skipped, pending))
		if skipped > 0 {
			sb.WriteString(fmt.Sprintf("(%d nodes were PRUNED because a dependency failed. Do NOT claim success for pruned work.)\n", skipped))
		}
		sb.WriteString("\n")
	}

	// Node returns from the gate (failures + resolved). The gate filters and
	// formats this; we just include the section.
	if gateCtx != nil {
		if returns := gateCtx.Sources[SourceNodeReturns]; returns != "" {
			sb.WriteString("## Node Results\n\n")
			sb.WriteString(returns)
			sb.WriteString("\n\n")
		}
		if wl := gateCtx.Sources[SourceWorklog]; wl != "" {
			sb.WriteString("## Execution Timeline\n\n```\n")
			sb.WriteString(wl)
			sb.WriteString("\n```\n\n")
		}
	}

	// Previous debug attempts — escalation hint
	if graph != nil {
		var attempts []string
		for _, gn := range graph.ResolvedByType(NodeMicroPlanner) {
			var mp struct {
				Summary string `json:"summary"`
			}
			if TryParseLLMJSON(gn.Result, &mp) && mp.Summary != "" {
				attempts = append(attempts, mp.Summary)
			}
		}
		if len(attempts) > 0 {
			sb.WriteString("## Previous Debug Attempts\n\n")
			sb.WriteString("The following debug fixes were already attempted and DID NOT solve the problem:\n\n")
			for i, att := range attempts {
				sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, att))
			}
			sb.WriteString("\nYour problem description MUST identify a DIFFERENT root cause.\n\n")
		}
	}

	return sb.String()
}

const interjectionReflectionPrompt = `You are a status classifier handling an operator message during an active investigation.
The operator's message is the PRIMARY input — address it directly.

Output JSON:
{
  "decision": "continue|conclude|investigate",
  "summary": "what happened and how you addressed the operator's message",
  "problem": "if investigate: describe what needs to change (for Holmes)",
  "verdict": "final answer (only if conclude)",
  "aggregate": true/false (only if conclude)
}

- "continue": operator's message is noted, current plan still makes sense
- "conclude": operator wants to stop, or evidence is sufficient
- "investigate": operator wants a different direction — describe the PROBLEM, not the solution
- Output ONLY the JSON, no commentary`

/*
 * fireInterjectionReflection runs a reflection checkpoint triggered by a human message.
 * desc: Unlike fireReflection, the human message is the primary focus, not a
 *       side-channel. Builds context from the operator message, gate-fetched
 *       evidence, and intent-filtered tools.
 * param: ctx - context for the LLM call.
 * param: rNode - the interjection reflection node in the graph.
 * param: graph - the investigation graph.
 * param: budget - the execution budget.
 * param: ch - channel to send the reflection's completion result.
 * param: trigger - the investigation trigger.
 * param: humanMsg - the operator's message text.
 * param: gateCtx - assembled context from ContextGate.
 * param: intent - optional IGX intent level.
 */
func (a *Agent) fireInterjectionReflection(ctx context.Context, rNode *Node, graph *Graph,
	budget *Budget, ch chan<- nodeCompletion, trigger Trigger, humanMsg string, gateCtx *ContextResponse, intent ...gates.Intent) {

	resolvedIntent := gates.Intent(0)
	if len(intent) > 0 {
		resolvedIntent = intent[0]
	}

	// Tool list for system prompt
	var toolSection strings.Builder
	toolSection.WriteString("\n## Available Tools (for re-investigation)\n")
	toolSection.WriteString(fmt.Sprintf("Only tools with impact ≤ %d (%s) will succeed.\n\n", int(resolvedIntent), resolvedIntent))
	for _, name := range a.registry.List() {
		skill, ok := a.registry.Get(name)
		if !ok {
			continue
		}
		rank := a.intentRegistry.ResolveToolIntent(name, skill, nil)
		if rank > int(resolvedIntent) {
			continue
		}
		toolSection.WriteString(fmt.Sprintf("- **%s**: %s — `%s`\n", name, skill.Description(), string(skill.Parameters())))
	}

	sysPrompt := interjectionReflectionPrompt + toolSection.String() + a.fleetSection()

	// User prompt — operator message first, then graph state
	var userBuf strings.Builder
	userBuf.WriteString("## Operator Message\n\n")
	userBuf.WriteString(humanMsg)
	userBuf.WriteString("\n\n")
	userBuf.WriteString(fmt.Sprintf("## Intent Level\n\n%s\n\n", resolvedIntent.String()))
	userBuf.WriteString(assembleReflectorPrompt(graph, gateCtx, trigger))

	messages := []llm.Message{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userBuf.String()},
	}

	started := time.Now()
	resp, err := a.executor.Complete(ctx, &llm.ChatRequest{
		Messages:    messages,
		Tools:       []llm.ToolDef{reflectorToolDef()},
		ToolChoice:  "required",
		Temperature: a.cfg.Temperature,
		MaxTokens:   1024,
	})

	trace := LLMTrace{
		AlertID:  trigger.AlertID,
		NodeID:   rNode.ID,
		NodeType: "interjection",
		Tag:      rNode.Tag,
		Model:    "executor",
		Started:  started,
		Input: map[string]string{
			"operator_message": humanMsg,
			"intent":           resolvedIntent.String(),
		},
		System:    sysPrompt,
		User:      userBuf.String(),
		LatencyMS: time.Since(started).Milliseconds(),
	}
	if gateCtx != nil {
		trace.GateReturned = gateCtx.Sources
	}

	if err != nil {
		trace.Err = err.Error()
		WriteLLMTrace(trace)
		ch <- nodeCompletion{NodeID: rNode.ID, Err: fmt.Errorf("interjection reflection LLM: %w", err)}
		return
	}

	raw, err := extractToolArgs(resp)
	if err != nil {
		trace.Err = err.Error()
		WriteLLMTrace(trace)
		ch <- nodeCompletion{NodeID: rNode.ID, Err: fmt.Errorf("interjection reflection: %w", err)}
		return
	}
	trace.Output = raw
	trace.TokensIn = resp.Usage.PromptTokens
	trace.TokensOut = resp.Usage.CompletionTokens
	WriteLLMTrace(trace)

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
	var output reflectionOutput
	if err := ParseLLMJSON(raw, &output); err != nil || output.Decision == "" {
		// Try finding a JSON object containing "decision" in the raw output
		if idx := strings.Index(raw, `"decision"`); idx >= 0 {
			for i := idx; i >= 0; i-- {
				if raw[i] == '{' {
					if err2 := ParseLLMJSON(raw[i:], &output); err2 == nil && output.Decision != "" {
						break
					}
				}
			}
		}
		if output.Decision == "" {
			if err != nil {
				return nil, fmt.Errorf("invalid reflection JSON: %w", err)
			}
			return nil, fmt.Errorf("reflection JSON missing decision field")
		}
	}

	switch output.Decision {
	case "continue", "conclude", "investigate":
		// valid
	default:
		return nil, fmt.Errorf("unknown reflection decision: %q", output.Decision)
	}

	// Normalize summary — use Reason as fallback (backward compat)
	if output.Summary == "" {
		output.Summary = output.Reason
	}

	// Parse verdict for conclude
	if len(output.RawVerdict) > 0 {
		var s string
		if json.Unmarshal(output.RawVerdict, &s) == nil {
			output.Verdict = s
		} else {
			output.Verdict = string(output.RawVerdict)
		}
	}
	if output.Decision == "conclude" && output.Verdict == "" {
		output.Verdict = output.Summary
	}

	return &output, nil
}
