package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/gates"
	"github.com/Compdeep/kaiju/internal/agent/llm"
)

/*
 * microPlannerOutput is the clean-room debugger's response.
 * desc: Contains a diagnosis summary and a plan of steps to fix the problem.
 */
type microPlannerOutput struct {
	Summary string     `json:"summary"` // diagnosis of root cause
	Nodes   []PlanStep `json:"nodes"`   // fix plan steps
}

/*
 * extractDebugSummary parses a debugger node's stored Result and returns the
 * diagnosis summary, head-truncated for worklog use.
 * desc: Used to write FIXED markers to the worklog after a debug cycle's
 *       grafted nodes complete successfully. Returns "" if the result can't
 *       be parsed.
 * param: raw - the debugger node's Result string.
 * return: truncated summary or empty string.
 */
func extractDebugSummary(raw string) string {
	if raw == "" {
		return ""
	}
	var out microPlannerOutput
	if err := ParseLLMJSON(raw, &out); err != nil {
		return ""
	}
	return Text.TruncateLog(out.Summary, 240)
}

/*
 * fireMicroPlanner runs the clean-room debugger.
 * desc: Called by the scheduler after Holmes concludes an investigation.
 *       Receives a curated context from ContextGate: a curator-built summary
 *       (worklog + failures filtered to the problem query) plus verbatim
 *       blueprint, workspace tree, and debug guidance. NO global worklog
 *       leakage — the curator only sees what relates to the query.
 *       Uses the reasoning model for deep analysis.
 * param: ctx - context for the LLM call.
 * param: mpNode - the micro-planner node in the graph.
 * param: graph - the investigation graph (used for budget tracking only).
 * param: budget - the execution budget.
 * param: ch - channel to send the completion result.
 * param: gateCtx - the assembled context from ContextGate (Summary + Sources).
 * param: trigger - the investigation trigger (used for original request text).
 * param: intent - the resolved investigation intent (post-auto-inference;
 *                 used for tool list filtering — must NOT be trigger.Intent()
 *                 which returns IntentAuto for chat queries and would filter
 *                 every tool out of the prompt).
 */
func (a *Agent) fireMicroPlanner(ctx context.Context, mpNode *Node, graph *Graph,
	budget *Budget, ch chan<- nodeCompletion, gateCtx *ContextResponse, trigger Trigger, intent gates.Intent) {

	sysPrompt := debuggerPrompt + a.fleetSection()
	userPrompt := assembleDebuggerPrompt(mpNode, gateCtx, trigger, a, intent)

	log.Printf("[dag] debugger prompt for %s (%d bytes): %s", mpNode.Tag, len(userPrompt), Text.TruncateLog(userPrompt, 800))

	messages := []llm.Message{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userPrompt},
	}

	log.Printf("[dag] debugger calling reasoning model for %s", mpNode.Tag)
	started := time.Now()

	resp, err := a.llm.Complete(ctx, &llm.ChatRequest{
		Messages:    messages,
		Tools:       []llm.ToolDef{debuggerToolDef()},
		ToolChoice:  "required",
		Temperature: a.cfg.Temperature,
		MaxTokens:   4096,
	})

	// Build trace entry for the debug log.
	trace := LLMTrace{
		AlertID:  trigger.AlertID,
		NodeID:   mpNode.ID,
		NodeType: "debugger",
		Tag:      mpNode.Tag,
		Model:    "reasoning",
		Started:  started,
		Input: map[string]string{
			"problem": fmt.Sprintf("%v", mpNode.Params["problem"]),
		},
		System:    sysPrompt,
		User:      userPrompt,
		LatencyMS: time.Since(started).Milliseconds(),
	}
	if gateCtx != nil {
		trace.GateSummary = gateCtx.Summary
		trace.GateReturned = gateCtx.Sources
	}

	if err != nil {
		trace.Err = err.Error()
		WriteLLMTrace(trace)
		ch <- nodeCompletion{NodeID: mpNode.ID, Err: fmt.Errorf("debugger LLM: %w", err)}
		return
	}

	raw, err := extractToolArgs(resp)
	if err != nil {
		trace.Err = err.Error()
		WriteLLMTrace(trace)
		ch <- nodeCompletion{NodeID: mpNode.ID, Err: fmt.Errorf("debugger: %w", err)}
		return
	}
	trace.Output = raw
	trace.TokensIn = resp.Usage.PromptTokens
	trace.TokensOut = resp.Usage.CompletionTokens
	WriteLLMTrace(trace)

	log.Printf("[dag] debugger output: %s", Text.TruncateLog(raw, 300))

	// Write debug blueprint to disk for observability (session-scoped).
	writeDebugBlueprint(a.cfg.MetadataDir, trigger.SessionID, mpNode.Tag, raw)

	ch <- nodeCompletion{NodeID: mpNode.ID, Result: raw, TokensIn: resp.Usage.PromptTokens, TokensOut: resp.Usage.CompletionTokens}
}

// assembleDebuggerPrompt formats the gate response into the debugger's user
// message. Order: original request → problem → Holmes RCA (if any) →
// curator summary → verbatim sources (blueprint, workspace_tree,
// skill_guidance) → available tools.
//
// When a Holmes RCA is present in mpNode.Params["rca"] it is rendered
// prominently as the authoritative diagnosis — the debugger is told (in the
// system prompt) to plan the fix from the RCA rather than re-diagnosing.
//
// intent must be the resolved investigation intent (NOT trigger.Intent() —
// for chat queries trigger.Intent() returns IntentAuto and would filter every
// tool out of the prompt, leaving the debugger with an empty list).
func assembleDebuggerPrompt(mpNode *Node, gateCtx *ContextResponse, trigger Trigger, a *Agent, intent gates.Intent) string {
	var sb strings.Builder

	sb.WriteString("## Original Request\n\n")
	sb.WriteString(formatTrigger(trigger))
	sb.WriteString("\n\n")

	if problem, ok := mpNode.Params["problem"].(string); ok && problem != "" {
		sb.WriteString("## Problem (as seen in the logs)\n\n")
		sb.WriteString(problem)
		sb.WriteString("\n\n")
	}

	// Holmes RCA — authoritative diagnosis from the investigator phase.
	// Stored as a JSON-marshaled string so it survives the params round-trip.
	if rcaRaw, ok := mpNode.Params["rca"].(string); ok && rcaRaw != "" {
		var rca RCAReport
		if err := json.Unmarshal([]byte(rcaRaw), &rca); err == nil {
			sb.WriteString("## Holmes's Root-Cause Analysis (authoritative)\n\n")
			sb.WriteString(fmt.Sprintf("**Root cause:** %s\n\n", rca.RootCause))
			if len(rca.Evidence) > 0 {
				sb.WriteString("**Evidence:**\n")
				for _, ev := range rca.Evidence {
					sb.WriteString("- ")
					sb.WriteString(ev)
					sb.WriteString("\n")
				}
				sb.WriteString("\n")
			}
			sb.WriteString(fmt.Sprintf("**Confidence:** %s\n\n", rca.Confidence))
			if rca.SuggestedStrategy != "" {
				sb.WriteString(fmt.Sprintf("**Suggested fix strategy:** %s\n\n", rca.SuggestedStrategy))
			}
			if len(rca.AffectedFiles) > 0 {
				sb.WriteString("**Affected files (fan the fix across ALL of these):**\n")
				for _, f := range rca.AffectedFiles {
					sb.WriteString("- ")
					sb.WriteString(f)
					sb.WriteString("\n")
				}
				sb.WriteString("\nEvery file listed here needs its own fix action — do not patch one and hope the others resolve. Choose the appropriate tool per file (compute for code edits, file_write for small configs, bash for renames or deletions).\n\n")
			}
			sb.WriteString("Plan a fix that directly addresses the named root cause. Do NOT re-diagnose.\n\n")
		}
	}

	if gateCtx != nil && gateCtx.Summary != "" {
		sb.WriteString("## Relevant Evidence (curated)\n\n")
		sb.WriteString(gateCtx.Summary)
		sb.WriteString("\n\n")
	}

	if gateCtx != nil {
		if bp := gateCtx.Sources[SourceBlueprint]; bp != "" {
			sb.WriteString("## Blueprint\n\n")
			sb.WriteString(bp)
			sb.WriteString("\n\n")
		}
		if tree := gateCtx.Sources[SourceWorkspaceTree]; tree != "" {
			sb.WriteString("## Workspace Files\n\n")
			sb.WriteString(tree)
			sb.WriteString("\n\n")
		}
		if dg := gateCtx.Sources[SourceSkillGuidance]; dg != "" {
			sb.WriteString("## Debug Guidance\n\n")
			sb.WriteString(dg)
			sb.WriteString("\n\n")
		}
	}

	// Available tools — filtered against the resolved investigation intent
	// (NOT trigger.Intent() — see func doc above). Tool parameter schemas
	// are included so the debugger emits correct param names instead of
	// hallucinating from training memory (no more `cmd` vs `command` drift).
	if a != nil {
		var toolSection strings.Builder
		toolSection.WriteString("## Available Tools\n\n")
		for _, name := range a.registry.List() {
			sk, ok := a.registry.Get(name)
			if !ok {
				continue
			}
			rank := a.intentRegistry.ResolveToolIntent(name, sk, nil)
			if rank > int(intent) {
				continue
			}
			toolSection.WriteString(fmt.Sprintf("- **%s**: %s — `%s`\n", name, sk.Description(), string(sk.Parameters())))
		}
		sb.WriteString(toolSection.String())
	}

	return sb.String()
}

// writeDebugBlueprint saves the debugger's plan to a _debug_ blueprint file
// within the session's blueprint directory.
func writeDebugBlueprint(metadataDir, sessionID, tag, content string) {
	dir := blueprintsDir(metadataDir, sessionID)
	os.MkdirAll(dir, 0755)
	name := fmt.Sprintf("_debug_%s_%d.blueprint.md", sanitizeTag(tag), time.Now().Unix())
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		log.Printf("[dag] failed to write debug blueprint: %v", err)
	} else {
		log.Printf("[dag] debug blueprint written: %s", path)
	}
}
