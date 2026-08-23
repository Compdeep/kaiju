package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/prompt"
)

/*
 * reflectionOutput is the structured response from a reflection checkpoint.
 * desc: Contains the reflection decision (continue/replan/conclude), optional
 *       outcome text, and the `next` move handed to the executive on replan.
 *       A stray "investigate" (removed decision) is coerced to "replan".
 */
type reflectionOutput struct {
	Decision   string          `json:"decision"`            // "continue", "replan", "conclude" ("investigate" coerced → "replan")
	Progress   string          `json:"progress,omitempty"`  // "productive", "diminishing", "stuck" — scheduler-consumed; "" defaults to productive
	Summary    string          `json:"summary"`             // status description
	Problem    string          `json:"problem,omitempty"`   // only for investigate: what's wrong (passed to Holmes)
	Next       string          `json:"next,omitempty"`      // only for replan: the concrete next step the executive should plan (steps succeeded and revealed more work)
	RawOutcome json.RawMessage `json:"outcome"`             // only for conclude — may be string or object
	Outcome    string          `json:"-"`                   // parsed from RawOutcome
	Reason     string          `json:"reason"`              // backward compat — used as Summary fallback
	Aggregate  *bool           `json:"aggregate,omitempty"` // only for conclude
}

/*
 * ReflectionBody is the typed output of a reflection (or interjection) node.
 * desc: Carries the full parsed decision so no field is lost at storage. This
 *       is the fix for the old conclude path, which stored only the outcome
 *       string and dropped Decision/Next/Summary/Aggregate from the node. Raw
 *       holds the reflector's original tool-call JSON for faithful rendering
 *       (Evidence) and template access (Field), matching pre-refactor behavior.
 */
type ReflectionBody struct {
	RawBacked
	Out reflectionOutput
}

// newReflectionBody keeps the whole reflection: the decision and its reasons,
// and the raw tool-call JSON behind them.
func newReflectionBody(out reflectionOutput, raw string) ReflectionBody {
	return ReflectionBody{RawBacked: RawBacked{Raw: raw}, Out: out}
}

// Evidence returns the reflector's raw JSON — the same text the pre-refactor
// continue/replan paths stored, now also carried on conclude so decision, next,
// summary and aggregate survive rather than collapsing to the outcome alone.
//
// Overrides the embedded RawBacked, which returns Raw unchanged. A reflection
// that produced no raw JSON still has a summary worth showing, and returning
// empty there would blank the trace line.
func (b ReflectionBody) Evidence() string {
	if b.Raw != "" {
		return b.Raw
	}
	return b.Out.Summary
}

// Summary renders a short "decision: reason" line for the frontend trace.
func (b ReflectionBody) Summary() string {
	reason := b.Out.Reason
	if reason == "" {
		reason = b.Out.Summary
	}
	reason = Text.TruncateLog(reason, 160)
	if b.Out.Decision == "" {
		return reason
	}
	return b.Out.Decision + ": " + reason
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

	defer a.guardNodeCompletion("fireReflection", rNode.ID, ch)

	// Prepend SOUL.md (identity + persistence litany) — same reach as the
	// aggregator. Without this the reflector lacks the "don't give up / don't
	// punt to other apps" cluster and takes the easy "conclude · too complex"
	// exit on hard queries.
	sysPrompt := ComposeSystemPrompt(a.soulPrompt, fmt.Sprintf(prompt.Reflector, a.FormatRule())) + a.environmentSection()
	// The scheduler stamps a plain-English budget line ("replan round 2 of 3,
	// 3m40s elapsed") into the reflection node's params so the reflector can
	// self-regulate. Empty for reflection sites that don't set it.
	budgetLine, _ := rNode.Params["budget"].(string)
	userPrompt := a.assembleReflectorPrompt(graph, gateCtx, trigger, budgetLine)

	// What the run has done so far, in the words of the request rather than as
	// a list of steps. The reader below is choosing between more work and
	// stopping, and the thing it most needs told is what is already in hand and
	// unused — see edge_reframe.go.
	sysPrompt, userPrompt = WithReframe(sysPrompt, userPrompt,
		a.EdgeReFrame(ctx, graph, a.formatTrigger(trigger),
			"decide whether this run should do more work or stop and answer"))
	messages := []llm.Message{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userPrompt},
	}

	// Surface the reflection checkpoint as a live node so the UI shows it running
	// (the executive/tool/aggregator nodes broadcast; reflection didn't until now).
	a.broadcastDAGEvent(graph, DAGEvent{Type: "node", SessionID: trigger.SessionID, NodeID: rNode.ID, Node: &NodeInfo{
		ID: rNode.ID, Type: "reflection", State: "running", Tag: "reflect",
	}})

	started := time.Now()
	reflectID := TraceID{NodeID: rNode.ID, NodeType: "reflector", Tag: rNode.Tag}
	if gateCtx != nil {
		reflectID.GateReturned = gateCtx.Sources
	}
	resp, err := a.completeLightChecked(withTrace(ctx, reflectID), &llm.ChatRequest{
		Messages:    messages,
		Tools:       []llm.ToolDef{reflectorToolDef()},
		ToolChoice:  "required",
		Temperature: a.cfg.Temperature,
		MaxTokens:   1024,
	})

	if err != nil {
		a.broadcastDAGEvent(graph, DAGEvent{Type: "node", SessionID: trigger.SessionID, NodeID: rNode.ID, Node: &NodeInfo{
			ID: rNode.ID, Type: "reflection", State: "failed", Tag: "reflect", Ms: time.Since(started).Milliseconds(), Error: err.Error(),
		}})
		ch <- nodeCompletion{NodeID: rNode.ID, Err: fmt.Errorf("reflection LLM: %w", err)}
		return
	}

	raw, err := extractToolArgs(resp)
	if err != nil {
		a.broadcastDAGEvent(graph, DAGEvent{Type: "node", SessionID: trigger.SessionID, NodeID: rNode.ID, Node: &NodeInfo{
			ID: rNode.ID, Type: "reflection", State: "failed", Tag: "reflect", Ms: time.Since(started).Milliseconds(), Error: err.Error(),
		}})
		ch <- nodeCompletion{NodeID: rNode.ID, Err: fmt.Errorf("reflection: %w", err)}
		return
	}

	log.Printf("[dag] reflection output: %s", Text.TruncateLog(raw, 200))

	a.broadcastDAGEvent(graph, DAGEvent{Type: "node", SessionID: trigger.SessionID, NodeID: rNode.ID, Node: &NodeInfo{
		ID: rNode.ID, Type: "reflection", State: "resolved", Tag: "reflect", Ms: time.Since(started).Milliseconds(),
	}})
	ch <- nodeCompletion{NodeID: rNode.ID, Result: raw, TokensIn: resp.Usage.PromptTokens, TokensOut: resp.Usage.CompletionTokens}
}

// assembleReflectorPrompt builds the reflector's user message from a gate
// response. Sections: Original Request, Budget, Graph Summary, FAILURES
// DETECTED, Evidence, Previous Debug Attempts. `budgetLine` is an optional
// plain-English budget line the scheduler passes so the reflector knows its
// position (replan round X of Y, elapsed) and self-regulates.
func (a *Agent) assembleReflectorPrompt(graph *Graph, gateCtx *ContextResponse, trigger Trigger, budgetLine string) string {
	var sb strings.Builder

	sb.WriteString("## Original Request\n\n")
	sb.WriteString(a.formatTrigger(trigger))
	sb.WriteString("\n\n")

	// ## History — the running record of this investigation: the round counter +
	// wall clock (the soft brake), then one compact line per prior replan and per
	// prior debug fix, so the reflector can SEE what earlier rounds already tried
	// and not loop on a move that already returned nothing. Merges what used to be
	// separate "Budget" and "Previous Debug Attempts" sections.
	{
		var replans, debugAttempts []string
		if graph != nil {
			replans = graph.ReplanRecords()
			for _, gn := range graph.ResolvedByType(NodeMicroPlanner) {
				var mp struct {
					Summary string `json:"summary"`
				}
				if TryParseLLMJSON(gn.Result, &mp) && mp.Summary != "" {
					debugAttempts = append(debugAttempts, mp.Summary)
				}
			}
		}
		if budgetLine != "" || len(replans) > 0 || len(debugAttempts) > 0 {
			sb.WriteString("## History\n\n")
			if budgetLine != "" {
				sb.WriteString(budgetLine)
				sb.WriteString("\n\n")
			}
			if len(replans) > 0 {
				sb.WriteString("Replans already tried — do NOT repeat a move that returned nothing or was blocked:\n")
				for _, r := range replans {
					sb.WriteString("- " + r + "\n")
				}
				sb.WriteString("\n")
			}
			if len(debugAttempts) > 0 {
				sb.WriteString("Debug fixes already attempted and DID NOT solve it — name a DIFFERENT root cause:\n")
				for i, att := range debugAttempts {
					sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, att))
				}
				sb.WriteString("\n")
			}
		}
	}

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

	return sb.String()
}

// interjectionReflectionPrompt moved to prompt.Interjection
// (internal/agent/prompt/prompts.md).

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
	a.toolSectionLines(&toolSection, int(resolvedIntent), agentToolName)

	sysPrompt := ComposeSystemPrompt(a.soulPrompt, prompt.Interjection) + toolSection.String() + a.environmentSection()

	// User prompt — operator message first, then graph state
	var userBuf strings.Builder
	userBuf.WriteString("## Operator Message\n\n")
	userBuf.WriteString(humanMsg)
	userBuf.WriteString("\n\n")
	userBuf.WriteString(fmt.Sprintf("## Intent Level\n\n%s\n\n", resolvedIntent.String()))
	userBuf.WriteString(a.assembleReflectorPrompt(graph, gateCtx, trigger, ""))

	messages := []llm.Message{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userBuf.String()},
	}

	interjectID := TraceID{
		NodeID:   rNode.ID,
		NodeType: "interjection",
		Tag:      rNode.Tag,
		Input: map[string]string{
			"operator_message": humanMsg,
			"intent":           resolvedIntent.String(),
		},
	}
	if gateCtx != nil {
		interjectID.GateReturned = gateCtx.Sources
	}
	resp, err := a.completeLightChecked(withTrace(ctx, interjectID), &llm.ChatRequest{
		Messages:    messages,
		Tools:       []llm.ToolDef{reflectorToolDef()},
		ToolChoice:  "required",
		Temperature: a.cfg.Temperature,
		MaxTokens:   1024,
	})

	if err != nil {
		ch <- nodeCompletion{NodeID: rNode.ID, Err: fmt.Errorf("interjection reflection LLM: %w", err)}
		return
	}

	raw, err := extractToolArgs(resp)
	if err != nil {
		ch <- nodeCompletion{NodeID: rNode.ID, Err: fmt.Errorf("interjection reflection: %w", err)}
		return
	}

	log.Printf("[dag] interjection reflection output: %s", Text.TruncateLog(raw, 200))

	ch <- nodeCompletion{NodeID: rNode.ID, Result: raw}
}

/*
 * parseReflectionOutput extracts the reflection decision from LLM output.
 * desc: Strips code fences, parses JSON, normalizes the outcome field
 *       (which may be a string or object), and validates the decision.
 *       Falls back to using reason as outcome if decision is "conclude"
 *       but outcome is empty.
 * param: raw - the raw LLM output string.
 * return: parsed reflectionOutput pointer, or error if JSON is invalid.
 */
func parseReflectionOutput(raw string) (*reflectionOutput, error) {
	var output reflectionOutput
	if err := ParseLLMJSON(raw, &output); err != nil || output.Decision == "" {
		// Try locating a JSON object containing "decision" in the raw output
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

	// `investigate` was removed as a decision — repair now flows through
	// `replan` → the executive plans a `debug` super-tool step. Coerce any
	// stray "investigate" (an old prompt or model) into `replan`, carrying its
	// problem statement into `next` so the executive still gets the failure.
	if output.Decision == "investigate" {
		output.Decision = "replan"
		if output.Next == "" {
			output.Next = output.Problem
		}
	}

	switch output.Decision {
	case "continue", "replan", "conclude":
		// valid
	default:
		return nil, fmt.Errorf("unknown reflection decision: %q", output.Decision)
	}

	// Normalize summary — use Reason as fallback (backward compat)
	if output.Summary == "" {
		output.Summary = output.Reason
	}

	// Parse outcome for conclude
	if len(output.RawOutcome) > 0 {
		var s string
		if json.Unmarshal(output.RawOutcome, &s) == nil {
			output.Outcome = s
		} else {
			output.Outcome = string(output.RawOutcome)
		}
	}
	if output.Decision == "conclude" && output.Outcome == "" {
		output.Outcome = output.Summary
	}

	return &output, nil
}
