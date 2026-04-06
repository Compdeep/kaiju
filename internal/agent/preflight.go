package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/user/kaiju/internal/agent/gates"
	"github.com/user/kaiju/internal/agent/llm"
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
}

// preflightCategories is the fixed set of tool categories the preflight
// call can name. The planner receives the list as a prompt hint and picks
// specific tools that satisfy the named categories.
var preflightCategories = []string{"network", "filesystem", "compute", "process", "info"}

const preflightSystemPrompt = `You are a query preflight analyst. Analyze the user's query and return structured metadata that downstream components will use to plan and execute the work. Output ONLY JSON, no commentary.

## Output schema

{
  "skills": ["skill_key", ...],
  "mode": "chat" | "meta" | "investigate",
  "intent": %s,
  "required_categories": ["network", "filesystem", "compute", "process", "info"]
}

## Field meanings

**skills** — Select the guidance skills whose domain matches the query. Zero or more from:
%s

**mode** — Your job is to help the user. Classify:
- "chat": greetings, thanks, small talk. Nothing to do.
- "meta": questions about what you can do.
- "investigate": the user wants something done. Build it, fix it, find it, run it. If they're asking for anything — investigate.

**intent** — Safety level the plan will need. Pick one:
%s

**required_categories** — Tool categories the plan MUST include. Pick from:
- "network": web fetch, web search, external APIs
- "filesystem": read, write, list files
- "compute": run code, write programs, analyze data (the compute tool)
- "process": manage system processes, services, daemons
- "info": system state, env vars, disk, network info

Return ONLY the raw JSON object.`

/*
 * preflightRaw mirrors the JSON shape emitted by the LLM. Parsed into PreflightResult.
 */
type preflightRaw struct {
	Skills             []string `json:"skills"`
	Mode               string   `json:"mode"`
	Intent             string   `json:"intent"`
	RequiredCategories []string `json:"required_categories"`
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
	// Build dynamic intent list from the registry: enum for schema + descriptions.
	intentNames := a.intentRegistry.AllowedNames(-1)
	intentEnum := `"` + strings.Join(intentNames, `" | "`) + `"`
	intentDescriptions := a.intentRegistry.PromptBlock(-1)
	sysPrompt := fmt.Sprintf(preflightSystemPrompt, intentEnum, manifest, intentDescriptions)

	// Build message list: system, then last N turns of history (assistant
	// replies truncated), then the current query.
	msgs := []llm.Message{{Role: "system", Content: sysPrompt}}
	msgs = append(msgs, truncatedHistory(history, 5, 500)...)
	msgs = append(msgs, llm.Message{Role: "user", Content: query})

	resp, err := a.executor.Complete(ctx, &llm.ChatRequest{
		Messages:    msgs,
		Temperature: 0.0,
		MaxTokens:   256,
	})
	if err != nil {
		log.Printf("[dag] preflight failed, using defaults: %v", err)
		return defaultPreflight()
	}
	if len(resp.Choices) == 0 {
		log.Printf("[dag] preflight returned no choices, using defaults")
		return defaultPreflight()
	}

	raw := resp.Choices[0].Message.Content
	cleaned := Text.StripCodeFence(raw)

	var out preflightRaw
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
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
