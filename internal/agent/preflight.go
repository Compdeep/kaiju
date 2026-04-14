package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Compdeep/kaiju/internal/agent/gates"
	"github.com/Compdeep/kaiju/internal/agent/llm"
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
}

// preflightCategories is the fixed set of tool categories the preflight
// call can name. The planner receives the list as a prompt hint and picks
// specific tools that satisfy the named categories.
var preflightCategories = []string{"network", "filesystem", "compute", "process", "info"}

const preflightSystemPrompt = `You are a query preflight analyst. Analyze the user's CURRENT query (the user message at the bottom of this conversation) and return structured metadata that downstream components will use to plan and execute the work. Output ONLY JSON, no commentary.

## What to classify vs. what is context

**The user message at the bottom of the conversation IS the query you classify.** That message is the only thing that drives the mode/intent decisions below.

**The "Prior Context" section in this system prompt (if present) is for PROJECT AWARENESS ONLY.** It contains the previous response this system gave the user — use it to understand what KIND of project is being worked on (web app, Python script, Go service, data analysis, etc.) so you can pick relevant skills. **Do NOT classify based on the prior context.** A short follow-up like "fix it" or "try again" still gets the SAME intent and mode as if there were no prior context — but the prior context tells you what skills the work touches.

If the user's query is "get this site working" and the prior context mentions a React + Vite webapp, the skills should include the webdeveloper skill (because the project is a webapp), but the mode/intent classification should be based only on the literal query "get this site working".

## Output schema

{
  "skills": ["skill_key", ...],
  "mode": "chat" | "meta" | "investigate",
  "intent": %s,
  "required_categories": ["network", "filesystem", "compute", "process", "info"],
  "context": "one sentence framing the user's intent for the executor"
}

## Field meanings

**skills** — Select the guidance skills whose domain matches the work being done. Use BOTH the user's query AND the prior context (if any) to pick. Zero or more from:
%s

**mode** — Based on the user's CURRENT query only, classify:
- "chat": ONLY greetings, thanks, small talk, or purely conversational messages with no actionable request. Examples: "hello", "thanks", "how are you".
- "meta": questions about your own capabilities. Examples: "what can you do", "what tools do you have".
- "investigate": the user wants ANYTHING done or answered. Download, build, fix, find, run, search, explain, create, analyze, install, deploy, check, test — if there is a verb asking you to act or produce information, it is investigate. When in doubt, choose investigate.

**intent** — Safety level the plan will need. Pick based on what ACTIONS the query implies, not how it's phrased. A user reporting a problem ("X isn't working", "I can't access Y") after prior work implicitly wants it fixed — that requires operate, not observe. Pick one:
%s

**required_categories** — Tool categories the plan MUST include. Pick from:
- "network": web fetch, web search, external APIs
- "filesystem": read, write, list files
- "compute": run code, write programs, analyze data (the compute tool)
- "process": manage system processes, services, daemons
- "info": system state, env vars, disk, network info

**context** — One sentence summarising what the user wants, using both the current query AND the prior context. This frames vague queries like "do it", "get more", "try again" for the executor. Be specific: "User wants to download more eurodance videos using yt-dlp" not "User wants more." Include the method/tool if the prior context established one.

Return ONLY the raw JSON object.`

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
}

/*
 * preflightQuery runs one executor-model LLM call to answer the four
 * pre-plan questions: skills, mode, intent, required categories.
 * desc: Builds a manifest of the unioned capability cards and SkillMD
 *       guidance, asks the model to classify the query, and validates the
 *       returned fields. Any missing or malformed field falls back to a
 *       safe default (empty list, "investigate", rank 0, []). The last
 *       few turns of conversation history are included so the model can
 *       resolve short follow-ups like "yeah do it" or "run that".
 * param: ctx - context for the LLM call.
 * param: query - the user query text.
 * param: history - conversation history (last N turns used; assistant
 *                  replies are truncated to keep the call small).
 * return: populated PreflightResult. Never returns nil; on failure, returns
 *         a neutral result so the caller can proceed with defaults.
 */
func (a *Agent) preflightQuery(ctx context.Context, query string, history []llm.Message) *PreflightResult {
	manifest := a.buildSkillManifest()
	log.Printf("[dag] preflight: manifest has %d capabilities + %d guidance skills", len(a.capabilities), len(a.skillGuidance))
	// Build dynamic intent list from the registry: enum for schema + descriptions.
	intentNames := a.intentRegistry.AllowedNames(-1)
	intentEnum := `"` + strings.Join(intentNames, `" | "`) + `"`
	intentDescriptions := a.intentRegistry.PromptBlock(-1)
	sysPrompt := fmt.Sprintf(preflightSystemPrompt, intentEnum, manifest, intentDescriptions)

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

	resp, err := a.executor.Complete(ctx, &llm.ChatRequest{
		Messages:    msgs,
		Tools:       []llm.ToolDef{preflightToolDef()},
		ToolChoice:  "required",
		Temperature: 0.0,
		MaxTokens:   256,
	})
	if err != nil {
		log.Printf("[dag] preflight failed, using defaults: %v", err)
		return defaultPreflight()
	}

	raw, err := extractToolArgs(resp)
	if err != nil {
		log.Printf("[dag] preflight returned no choices, using defaults")
		return defaultPreflight()
	}
	var out preflightRaw
	if err := ParseLLMJSON(raw, &out); err != nil {
		log.Printf("[dag] preflight parse failed (%v), using defaults", err)
		return defaultPreflight()
	}

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
