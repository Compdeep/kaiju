package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/gates"
	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/agent/prompt"
)

/*
 * PreflightResult is the structured output of the pre-plan LLM call.
 * desc: One executor-model call answers multiple classification questions
 *       at once so downstream components (planner, scheduler, aggregator)
 *       have the decisions pre-made instead of each running its own
 *       classifier.
 */
type PreflightResult struct {
	Skills             []string     // which guidance cards/skills apply (union of capabilities + skillGuidance)
	Mode               string       // "chat" | "meta" | "investigate"
	Intent             gates.Intent // inferred intent rank from the registry (used when trigger intent is Auto)
	RequiredCategories []string     // tool categories the plan must include (network/filesystem/compute/process/info)
	Context            string       // one-line framing of the user's intent based on conversation history
	ComputeMode        string       // "" (no compute / no opinion) | "shallow" | "deep" — authoritative for the planner
}

// preflightCategories is the fixed set of tool categories the preflight
// call can name. The planner receives the list as a prompt hint and picks
// specific tools that satisfy the named categories.
var preflightCategories = []string{"network", "filesystem", "compute", "process", "info"}

// preflightSystemPromptWithContext is built at call time when prior context
// is available — appended after the base prompt. Kept separate so the base
// prompt const stays clean.
const preflightPriorContextTemplate = `

## Prior Context (project awareness only — do NOT classify based on this)

The previous response this system gave to the user was:

%s

Use this context to identify what KIND of project is being worked on and pick appropriate skills. The project type tells you which skills are relevant. Do NOT use this to decide mode or intent — only the user's current query (the user message below) drives those decisions.`

/*
 * preflightRaw mirrors the JSON shape emitted by the LLM. Parsed into PreflightResult.
 */
type preflightRaw struct {
	Skills             []string `json:"skills"`
	Mode               string   `json:"mode"`
	Intent             string   `json:"intent"`
	RequiredCategories []string `json:"required_categories"`
	Context            string   `json:"context"`
	ComputeMode        string   `json:"compute_mode"`
}

/*
 * routeQuery is the cheap first pass: it decides the handling mode
 * (chat / meta / investigate) with a tiny prompt and NO skill manifest. Only the
 * agentic path then pays for the full classify + skill selection, so a "hello"
 * never loads the skills it won't use. Fails safe to "investigate" so a real
 * request is never misrouted to chat.
 */
func (a *Agent) routeQuery(ctx context.Context, alertID, query string) string {
	started := time.Now()
	trace := LLMTrace{AlertID: alertID, NodeType: "preflight", Tag: "route", Started: started, System: prompt.Route, User: query}
	resp, err := a.completeLight(ctx, &llm.ChatRequest{
		Messages:    []llm.Message{{Role: "system", Content: prompt.Route}, {Role: "user", Content: query}},
		Tools:       []llm.ToolDef{routeToolDef()},
		ToolChoice:  "required",
		Temperature: 0.0,
		MaxTokens:   16,
	})
	trace.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		trace.Err = err.Error()
		WriteLLMTrace(trace)
		return "investigate"
	}
	raw, err := extractToolArgs(resp)
	if err != nil {
		trace.Err = "no tool args returned"
		WriteLLMTrace(trace)
		return "investigate"
	}
	trace.Output = raw
	var out struct {
		Mode string `json:"mode"`
	}
	if err := ParseLLMJSON(raw, &out); err != nil {
		trace.Err = "parse failed: " + err.Error()
		WriteLLMTrace(trace)
		return "investigate"
	}
	WriteLLMTrace(trace)
	switch out.Mode {
	case "chat", "meta", "investigate":
		return out.Mode
	default:
		return "investigate"
	}
}

/*
 * preflightQuery is the entry point. It routes first (cheap, no skill manifest).
 * A pure chat/meta message returns immediately with no skills selected — so
 * neither this step nor the downstream chat reply pays for skills it won't use.
 * Agentic queries fall through to the full classification.
 */
func (a *Agent) preflightQuery(ctx context.Context, alertID, query string, history []llm.Message) *PreflightResult {
	switch a.routeQuery(ctx, alertID, query) {
	case "chat":
		log.Printf("[dag] route: chat — skipped skill classification")
		return &PreflightResult{Mode: "chat"}
	case "meta":
		log.Printf("[dag] route: meta — skipped skill classification")
		return &PreflightResult{Mode: "meta"}
	}
	return a.classifyInvestigate(ctx, alertID, query, history)
}

/*
 * classifyInvestigate runs one executor-model LLM call to answer the pre-plan
 * questions for an AGENTIC query: skills, intent, required categories, context,
 * compute mode. The skill manifest is built here — only reached on the
 * investigate path. Any missing/malformed field falls back to a safe default.
 */
func (a *Agent) classifyInvestigate(ctx context.Context, alertID, query string, history []llm.Message) *PreflightResult {
	manifest := a.buildSkillManifest()
	log.Printf("[dag] preflight: manifest has %d capabilities + %d guidance skills", len(a.capabilities), len(a.skillGuidance))
	// Build dynamic intent list from the registry: enum for schema + descriptions.
	intentNames := a.intentRegistry.AllowedNames(-1)
	intentEnum := `"` + strings.Join(intentNames, `" | "`) + `"`
	intentDescriptions := a.intentRegistry.PromptBlock(-1)
	sysPrompt := fmt.Sprintf(prompt.Preflight, intentEnum, manifest, intentDescriptions)

	// Project-awareness pass: pull the most recent assistant message from
	// history (that's the previous aggregator response) and inject it into
	// the system prompt as a clearly-labeled Prior Context block. This lets
	// the LLM know what KIND of project is being worked on without
	// confusing the classification target. The current user query stays as
	// the only user-role message — so the LLM has an unambiguous "this is
	// what to classify" signal.
	if priorContext := lastAssistantMessage(history); priorContext != "" {
		// Generous truncation — 1500 chars is enough to fit a typical
		// aggregator verdict including its project-type description.
		sysPrompt += fmt.Sprintf(preflightPriorContextTemplate, Text.TruncateLog(priorContext, 1500))
		log.Printf("[dag] preflight: injected prior context (%d chars truncated to 1500)", len(priorContext))
	}

	// Build message list: system prompt (with optional prior context) then
	// the current user query as the ONLY user message. We deliberately do
	// not pass the older history-as-messages anymore — the prior context
	// in the system prompt is the structured replacement, and the strict
	// "what to classify" signal is now unambiguous.
	msgs := []llm.Message{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: query},
	}
	log.Printf("[dag] preflight: query=%q, history=%d turns (last assistant injected as context)", query, len(history))

	started := time.Now()
	trace := LLMTrace{
		AlertID:  alertID,
		NodeType: "preflight",
		Tag:      "classify",
		Started:  started,
		System:   sysPrompt,
		User:     query,
	}

	resp, err := a.completeLight(ctx, &llm.ChatRequest{
		Messages:    msgs,
		Tools:       []llm.ToolDef{preflightToolDef()},
		ToolChoice:  "required",
		Temperature: 0.0,
		MaxTokens:   256,
	})
	trace.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		log.Printf("[dag] preflight failed, using defaults: %v", err)
		trace.Err = err.Error()
		WriteLLMTrace(trace)
		return defaultPreflight()
	}

	raw, err := extractToolArgs(resp)
	if err != nil {
		log.Printf("[dag] preflight returned no choices, using defaults")
		trace.Err = "no tool args returned"
		WriteLLMTrace(trace)
		return defaultPreflight()
	}
	trace.Output = raw
	var out preflightRaw
	if err := ParseLLMJSON(raw, &out); err != nil {
		log.Printf("[dag] preflight parse failed (%v), using defaults", err)
		trace.Err = "parse failed: " + err.Error()
		WriteLLMTrace(trace)
		return defaultPreflight()
	}

	WriteLLMTrace(trace)
	return a.validatePreflight(&out)
}

/*
 * buildSkillManifest builds the "key: description" listing shown to the
 * preflight LLM so it can pick relevant skills. Unions capability cards
 * and guidance-only SkillMD entries.
 * return: formatted manifest string, or a placeholder if neither registry
 *         has content.
 */
func (a *Agent) buildSkillManifest() string {
	if len(a.capabilities) == 0 && len(a.skillGuidance) == 0 {
		return "(no skills available)"
	}
	var sb strings.Builder
	seen := make(map[string]bool)
	for _, card := range a.capabilities {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", card.Key, card.Description))
		seen[card.Key] = true
	}
	for name, s := range a.skillGuidance {
		if seen[name] {
			continue
		}
		sb.WriteString(fmt.Sprintf("- %s: %s\n", name, s.Description()))
	}
	return sb.String()
}

/*
 * validatePreflight normalizes and validates the raw preflight output.
 * desc: Filters skills to ones that exist in either registry, clamps mode
 *       and intent to known values, and filters required_categories to the
 *       canonical enum. Invalid fields fall back to safe defaults.
 * param: raw - unvalidated preflight output from the LLM.
 * return: validated PreflightResult.
 */
func (a *Agent) validatePreflight(raw *preflightRaw) *PreflightResult {
	out := &PreflightResult{
		Mode:   "investigate",
		Intent: gates.Intent(0),
	}

	// Skills — keep only keys that exist in either registry
	for _, key := range raw.Skills {
		if _, ok := a.capabilities[key]; ok {
			out.Skills = append(out.Skills, key)
			continue
		}
		if _, ok := a.skillGuidance[key]; ok {
			out.Skills = append(out.Skills, key)
			continue
		}
		log.Printf("[dag] preflight: unknown skill %q, dropping", key)
	}

	// Mode — must be one of the three
	switch strings.ToLower(strings.TrimSpace(raw.Mode)) {
	case "chat":
		out.Mode = "chat"
	case "meta":
		out.Mode = "meta"
	case "investigate", "":
		out.Mode = "investigate"
	default:
		log.Printf("[dag] preflight: unknown mode %q, defaulting to investigate", raw.Mode)
	}

	// Intent — resolve via the registry. Unknown names keep the safe default (rank 0).
	if name := strings.ToLower(strings.TrimSpace(raw.Intent)); name != "" {
		if i, ok := a.intentRegistry.ByName(name); ok {
			out.Intent = gates.Intent(i.Rank)
		} else {
			log.Printf("[dag] preflight: unknown intent %q, defaulting to rank 0", raw.Intent)
		}
	}

	// Required categories — keep only canonical enum values
	validCat := make(map[string]bool, len(preflightCategories))
	for _, c := range preflightCategories {
		validCat[c] = true
	}
	for _, c := range raw.RequiredCategories {
		normalized := strings.ToLower(strings.TrimSpace(c))
		if validCat[normalized] {
			out.RequiredCategories = append(out.RequiredCategories, normalized)
		} else if normalized != "" {
			log.Printf("[dag] preflight: unknown category %q, dropping", c)
		}
	}

	// Context — pass through as-is (freeform text).
	out.Context = strings.TrimSpace(raw.Context)

	// ComputeMode — tri-state: "" | "shallow" | "deep". Unknown values drop
	// to "" so the planner treats it as "no opinion" rather than guessing.
	switch strings.ToLower(strings.TrimSpace(raw.ComputeMode)) {
	case "deep":
		out.ComputeMode = "deep"
	case "shallow":
		out.ComputeMode = "shallow"
	case "":
		out.ComputeMode = ""
	default:
		log.Printf("[dag] preflight: unknown compute_mode %q, defaulting to none", raw.ComputeMode)
	}

	return out
}

/*
 * defaultPreflight returns a neutral preflight result used when the LLM
 * call fails or returns garbage.
 * desc: Safe defaults: no skills, investigate mode, rank 0 intent, no
 *       category requirements. The planner proceeds as if no preflight
 *       hints were given.
 * return: neutral PreflightResult.
 */
func defaultPreflight() *PreflightResult {
	return &PreflightResult{
		Mode:   "investigate",
		Intent: gates.Intent(0),
	}
}

/*
 * lastAssistantMessage returns the content of the most recent assistant
 * message in the history, or empty string if none exists. Used by the
 * preflight to surface the previous aggregator response as project context.
 * desc: Walks the history slice from the end and returns the first message
 *       with role "assistant". Returns empty if the history is empty or
 *       contains no assistant messages.
 * param: history - the full conversation history.
 * return: assistant message content, or empty string.
 */
func lastAssistantMessage(history []llm.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" && history[i].Content != "" {
			return history[i].Content
		}
	}
	return ""
}

/*
 * truncatedHistory returns the last maxTurns messages from history, with
 * assistant replies truncated to maxAssistantChars.
 * desc: Preflight needs just enough context to resolve short follow-ups
 *       like "yeah do it" without blowing up the input. User messages are
 *       the real signal so they stay whole; assistant replies tend to be
 *       long explanations and get clipped.
 * param: history - full conversation history.
 * param: maxTurns - how many recent messages to include.
 * param: maxAssistantChars - truncation limit for assistant messages.
 * return: truncated message slice ready to splice into the preflight call.
 */
func truncatedHistory(history []llm.Message, maxTurns, maxAssistantChars int) []llm.Message {
	if len(history) == 0 {
		return nil
	}
	start := len(history) - maxTurns
	if start < 0 {
		start = 0
	}
	recent := history[start:]
	out := make([]llm.Message, 0, len(recent))
	for _, msg := range recent {
		if msg.Role == "assistant" && len(msg.Content) > maxAssistantChars {
			out = append(out, llm.Message{
				Role:    msg.Role,
				Content: msg.Content[:maxAssistantChars] + "…",
			})
			continue
		}
		out = append(out, msg)
	}
	return out
}
