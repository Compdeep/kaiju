package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/prompt"
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
	Mode               string       // "chat" | "agent"
	Intent             gates.Intent // inferred intent rank from the registry (used when trigger intent is Auto)
	RequiredCategories []string     // tool categories the plan must include (network/filesystem/compute/process/info)
	Context            string       // one-line framing of the user's intent based on conversation history
	ComputeMode        string       // "" (no compute / no opinion) | "shallow" | "deep" — authoritative for the planner
	NeedsSynthesis     bool         // true ⇒ the run must end with the aggregator (a written synthesis), not a short reflector summary — set for deep/multi-source research and "build a section/report" tasks

	// LackingContext is what the router asked to have looked up in the earlier
	// messages, because answering needs something said before the part the model
	// can see. Empty is the usual answer and means nothing was missing. Only the
	// router fills it; the classify call has no opinion about it.
	LackingContext []string
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
	NeedsSynthesis     bool     `json:"needs_synthesis"`
}

// mentionsLiveInventory reports whether the query asks about the actual plugins
// or tools available right now — the class the route model keeps mis-sending to
// chat. "what can you do" is deliberately NOT here: that's a general-capability
// question the chat lane answers from the persona; only questions about the LIVE
// inventory need the agent (which calls plugin_list).
func mentionsLiveInventory(query string) bool {
	q := strings.ToLower(query)
	if strings.Contains(q, "plugin") {
		return true
	}
	for _, p := range []string{"your tools", "what tools", "which tools", "tools do you", "tools available", "tools can you"} {
		if strings.Contains(q, p) {
			return true
		}
	}
	return false
}

// asksToTakePrivilege reports a request to TAKE privilege on this machine, as
// opposed to a question about how privilege is taken.
//
// The route model classifies this one category wrong and nothing else. Measured
// over 84 queries in twelve groups, five runs each: every group answered
// identically all five times and correctly — robotics, research, machine state,
// generation, sysadmin, messaging, and five kinds of conversation, 385 of 385.
// Privilege escalation answered "chat" for six of its seven, none of them
// marginal — "get me a root shell", "become root on this box" and "can you gain
// root on this server" were 0 for 5.
//
// It is not the subject. "scan the network for open ports" and "crack the
// password on this zip file" route to the agent every time. It is not the
// phrasing either: "escalate to root on this vm" routes correctly until the user
// adds why they want it, and "(this is a white hacking test, also I own this vm)"
// takes it from 5 of 5 to 1 of 5.
//
// Four prompt rewrites were measured against it — dropping the narrow examples
// from the action bullet, saying that explaining a reason does not make a request
// hypothetical, reframing the whole question as whether tools would serve the
// request better, and adding a fuller list of what counts as an action. All four
// landed inside the run-to-run noise. So it is decided here rather than asked,
// the same as the inventory case above.
//
// The exclusions matter more here than they do there. A question about how
// privilege escalation works is answered from knowledge, and sending it to the
// agent would have the machine try it rather than explain it — which is worse
// than the extra call a stray route usually costs.
func asksToTakePrivilege(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	for _, frame := range []string{
		"how do i", "how do you", "how does", "how would i", "how would you",
		"what is", "what are", "what does", "why does", "why is",
		"explain", "describe", "tell me about",
	} {
		if strings.HasPrefix(q, frame) {
			return false
		}
	}
	for _, p := range []string{
		"gain root", "get root", "become root", "as root", "to root",
		"root shell", "root access", "root privile", "root user",
		"privilege escalation", "privilege escalat", "escalate privile",
		"elevate privile", "privesc", "sudo access", "administrator access",
	} {
		if strings.Contains(q, p) {
			return true
		}
	}
	return false
}

/*
 * routeQuery is the cheap first pass: it decides the handling mode
 * (chat / investigate) with a tiny prompt and NO skill manifest. Only the
 * agentic path then pays for the full classify + skill selection, so a "hello"
 * never loads the skills it won't use. Fails safe to "chat" (the cheap lane) on
 * any classifier error.
 */
func (a *Agent) routeQuery(ctx context.Context, triggerID, query string, history []llm.Message) (string, []string) {
	if a.classifyStub != nil {
		pf := a.classifyStub(query, history)
		return pf.Mode, pf.LackingContext
	}
	// Deterministic override — decided in code, NOT by the classifier. A question
	// about the live plugin/tool inventory ("do you have plugins?", "what tools do
	// you have") must run the agent so it calls plugin_list: the real answer is in
	// the registry, not the model's training. The route model reliably mis-reads
	// these as answerable-from-self no matter the prompt (proven across three prompt
	// rewrites), so we decide it here instead of asking. Over-routing is safe — a
	// stray investigate costs one extra call; a missed one returns a hallucinated
	// capability list.
	if mentionsLiveInventory(query) {
		log.Printf("[route] deterministic → agent (asks about live plugin/tool inventory)")
		return "agent", nil
	}
	if asksToTakePrivilege(query) {
		log.Printf("[route] deterministic → agent (asks to take privilege on this machine)")
		return "agent", nil
	}
	// Give the router just enough context to interpret a terse follow-up, then the
	// current message. See routeContext for what's included (summary + last turn).
	msgs := []llm.Message{{Role: "system", Content: prompt.Route}}
	msgs = append(msgs, routeContext(history)...)
	msgs = append(msgs, llm.Message{Role: "user", Content: query})
	ctx = withTrace(ctx, TraceID{NodeType: "preflight", Tag: "route"})
	resp, err := a.completeRoute(ctx, &llm.ChatRequest{
		Messages:    msgs,
		Tools:       []llm.ToolDef{routeToolDef()},
		ToolChoice:  "required",
		Temperature: 0.0,
		// Room for the mode and a handful of words to look up. It was 16, which
		// fits the mode alone: a reply carrying anything else would be cut part
		// way through and fail to parse, taking the routing decision with it.
		MaxTokens: 96,
	})
	// On ANY classifier failure — the model errored, refused, or returned
	// unparseable output — fall back to "chat", which the ROUTE prompt itself calls
	// the default and common case. Escalating to the agent should be a POSITIVE
	// decision, not what happens when the router trips. An aligned route model often
	// balks at classifying edge content (e.g. adult roleplay), and defaulting that
	// balk to "investigate" wrongly forced those turns onto the agent path — where
	// the (also aligned) planner then refused. Failing toward the cheap, safe
	// conversational lane keeps the user's selected chat model in play.
	if err != nil {
		return "chat", nil
	}
	raw, err := extractToolArgs(resp)
	if err != nil {
		traceFault(ctx, "no tool args returned")
		return "chat", nil
	}
	var out struct {
		Mode    string   `json:"mode"`
		Lacking []string `json:"lacking_context"`
	}
	if err := ParseLLMJSON(raw, &out); err != nil {
		traceFault(ctx, "parse failed: "+err.Error())
		return "chat", nil
	}
	lacking := cleanTerms(out.Lacking)
	switch out.Mode {
	case "chat", "agent":
		return out.Mode, lacking
	default:
		return "chat", lacking
	}
}

// cleanTerms drops the blanks and the repeats from what the router asked for.
//
// A model listing the same word twice, or offering an empty string among real
// ones, would otherwise widen the search expression without widening what it
// finds.
func cleanTerms(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	return out
}

// routeContext returns a MINIMAL slice of chat history for the router: the running
// "[Conversation summary]" system message (if compaction produced one) plus the
// previous user↔assistant exchange, the assistant reply capped. History ends with
// the CURRENT user message, so that last entry is dropped (it's passed separately
// as the query). Enough to resolve terse follow-ups ("try again", "now compare
// them") without bloating a 16-token classification.
func routeContext(history []llm.Message) []llm.Message {
	if len(history) <= 1 {
		return nil
	}
	var out []llm.Message
	for _, m := range history {
		if m.Role == "system" && strings.HasPrefix(m.Content, "[Conversation summary]") {
			out = append(out, m)
			break
		}
	}
	prior := history[:len(history)-1] // drop the current user message
	start := len(prior) - 2
	if start < 0 {
		start = 0
	}
	for _, m := range prior[start:] {
		if m.Role == "assistant" && len(m.Content) > 500 {
			m.Content = m.Content[:500] + "…"
		}
		out = append(out, m)
	}
	return out
}

// The fused preflightQuery wrapper (route + classify in one call) was split: the
// scheduler now calls routeQuery and classifyInvestigate explicitly, so routing
// and plan-prep are independent — autonomous mode skips routing entirely.

/*
 * classifyInvestigate runs one executor-model LLM call to answer the pre-plan
 * questions for an AGENTIC query: skills, intent, required categories, context,
 * compute mode. The skill manifest is built here — only reached on the
 * investigate path. Any missing/malformed field falls back to a safe default.
 */
func (a *Agent) classifyInvestigate(ctx context.Context, triggerID, query string, history []llm.Message) *PreflightResult {
	if a.classifyStub != nil {
		return a.classifyStub(query, history)
	}
	manifest := a.buildSkillManifest()
	log.Printf("[dag] preflight: manifest has %d skill cards", len(a.skillGuidance))
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
		// aggregator outcome including its project-type description.
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

	ctx = withTrace(ctx, TraceID{NodeType: "preflight", Tag: "classify"})
	resp, err := a.completeLight(ctx, &llm.ChatRequest{
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
		traceFault(ctx, "no tool args returned")
		return defaultPreflight()
	}
	var out preflightRaw
	if err := ParseLLMJSON(raw, &out); err != nil {
		log.Printf("[dag] preflight parse failed (%v), using defaults", err)
		traceFault(ctx, "parse failed: "+err.Error())
		return defaultPreflight()
	}

	return a.validatePreflight(&out)
}

/*
 * buildSkillManifest builds the "key: description" listing shown to the
 * preflight LLM so it can pick relevant skills. Unions skill card
 * and guidance-only SkillMD entries.
 * return: formatted manifest string, or a placeholder if neither registry
 *         has content.
 */
func (a *Agent) buildSkillManifest() string {
	if len(a.skillGuidance) == 0 {
		return "(no skills available)"
	}
	// Sorted, so the list the model chooses from reads the same way twice.
	var sb strings.Builder
	for _, key := range a.guidanceKeys() {
		if s, ok := a.skillGuidance[key]; ok {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", key, s.Description()))
		}
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
		Mode:   "agent",
		Intent: gates.Intent(0),
	}

	// Skills — keep only keys that exist in either registry, DE-DUPLICATED and
	// capped. A non-selective preflight otherwise repeats skills and attaches
	// most of the library to one step: prompt bloat, a noisy "guided by" list,
	// and irrelevant work (e.g. a system_operations skill dragging net_info into
	// a market-research run). The prompt asks for a focused few; these are the
	// backstops that hold even when the model over-lists.
	const maxSkills = 6
	seen := map[string]bool{}
	for _, key := range raw.Skills {
		if seen[key] {
			continue // duplicate — the model listed it twice
		}
		if _, ok := a.skillGuidance[key]; !ok {
			log.Printf("[dag] preflight: unknown skill %q, dropping", key)
			continue
		}
		seen[key] = true
		out.Skills = append(out.Skills, key)
		if len(out.Skills) >= maxSkills {
			log.Printf("[dag] preflight: capped skills at %d (model listed %d)", maxSkills, len(raw.Skills))
			break
		}
	}

	// Mode — must be one of the three
	switch strings.ToLower(strings.TrimSpace(raw.Mode)) {
	case "chat":
		out.Mode = "chat"
	case "agent", "":
		out.Mode = "agent"
	default:
		log.Printf("[dag] preflight: unknown mode %q, defaulting to agent", raw.Mode)
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

	// NeedsSynthesis — pass through; the aggregate decision reads it (with a
	// structural fan-out floor as backstop).
	out.NeedsSynthesis = raw.NeedsSynthesis

	return out
}

/*
 * defaultPreflight returns a neutral preflight result used when the LLM
 * call fails or returns garbage.
 * desc: Safe defaults: no skills, agent mode, rank 0 intent, no
 *       category requirements. The planner proceeds as if no preflight
 *       hints were given.
 * return: neutral PreflightResult.
 */
func defaultPreflight() *PreflightResult {
	return &PreflightResult{
		Mode:   "agent",
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
