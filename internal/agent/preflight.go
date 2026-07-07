package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

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
	ComputeMode        string       // "" (no compute / no opinion) | "shallow" | "deep" — authoritative for the planner
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

## CRITICAL: identifier preservation in context

The "context" field is the ONLY way concrete details from chat history reach downstream tasks. The Coder, Executive, and microplanner cannot see the conversation — they see only your context paragraph. **Anything you paraphrase, generalise, or omit is permanently lost for this turn.**

You MUST quote verbatim every concrete identifier present in the user's query OR the prior context:
- URLs (full, with query params, even if long): https://www.murc-kawasesouba.jp/fx/past_3month_result.php?y=2025&m={month}&d={day}&c=366551
- File paths: uploads/<session>/data.csv, project/script.py
- HTML/CSS selectors and table classes: <table class="data-table5">, #main
- API endpoints, function names, column/field names: TTM, 金額, update_csv_with_exchange_rates
- Exact constants and rules: "5 second delay between requests", "round to 2 decimals", "skip rows where col 9 is empty"
- Specific data shapes the user described

If the prior context contains URLs or selectors and the current query is "try again" or "do it", you MUST repeat those URLs/selectors verbatim in your context paragraph — otherwise the executive plans against an empty spec and the Coder hallucinates.

Failure mode to avoid: "the user wants to update the CSV with the **correct URLs**" — "correct URLs" is a paraphrase. The downstream LLM does not know which URLs. Quote them.

A long context paragraph is fine. A lossy short one is a bug.

## Output schema

{
  "skills": ["skill_key", ...],
  "mode": "chat" | "meta" | "investigate",
  "intent": %s,
  "required_categories": ["network", "filesystem", "compute", "process", "info"],
  "context": "paragraph covering intent + every concrete identifier (URLs, paths, selectors, constants) verbatim",
  "compute_mode": "" | "shallow" | "deep"
}

## Field meanings

**skills** — Select the guidance skills whose domain matches the work being done. Use BOTH the user's query AND the prior context (if any) to pick. Zero or more from:
%s

**mode** — Based on the user's CURRENT query only, classify:
- "chat": ONLY pure social messages with zero actionable content. Greetings ("hello", "hey"), thanks ("ty", "thanks"), farewells ("bye", "see you"), or trivial acknowledgements ("ok", "got it", "cool"). NOTHING ELSE qualifies as chat.
- "meta": questions about your own capabilities. Examples: "what can you do", "what tools do you have", "how do you work".
- "investigate": EVERYTHING ELSE — including complaints, diagnostic comments, frustration, follow-up clarifications, imperatives, questions, hypotheticals. If the user mentions a task, an output, a tool, a file, a website, a number, a name, or expresses ANY desire (explicit or implied), it is investigate. Examples that are investigate, not chat: "you didn't fetch the data", "use your compute", "you have python try again", "I told you to do X", "isn't it possible to Y", "what about Z". When in doubt, ALWAYS choose investigate. Misclassifying chat as investigate costs one extra LLM call; misclassifying investigate as chat blocks the user's actual request.

**intent** — Safety level the plan will need. Pick based on what ACTIONS the query implies, not how it's phrased. A user reporting a problem ("X isn't working", "I can't access Y") after prior work implicitly wants it fixed — that requires operate, not observe. Pick one:
%s

**required_categories** — Tool categories the plan MUST include. Pick from:
- "network": web fetch, web search, external APIs
- "filesystem": read, write, list files
- "compute": run code, write programs, analyze data (the compute tool)
- "process": manage system processes, services, daemons
- "info": system state, env vars, disk, network info

**context** — A paragraph covering what the user wants AND every concrete identifier needed to complete the task. Length should match what the task actually requires — short for trivial requests, longer for tasks with URLs/selectors/constants. See the "CRITICAL: identifier preservation" section above for the verbatim-quoting rule.

**compute_mode** — Whether the plan will need a compute node. **Default to "" — the aggregator (an LLM call) handles small math, ranking, summarisation, and reasoning over gathered evidence on its own.** Compute is overhead: it spawns a coder LLM, writes a script, runs it, captures stdout. Only escalate when the LLM cannot reliably do the job.

Set "shallow" ONLY when at least one of these is true:
- A library is required to do the work (numpy, scipy, pandas, sgp4, skyfield, pyephem, astropy, BeautifulSoup, jq, etc.) — the LLM can't run code.
- The data is too large for LLM context (CSV with thousands of rows, log files, big JSON dumps).
- Precision matters in a way the LLM is bad at (financial math, exact floating-point, date arithmetic, sgp4 propagation, statistical inference).
- The user explicitly asked for code, a script, a deliverable file, or repeatable/auditable output.
- The output needs to feed another tool as a real value (not prose).

**Lookup-shaped but compute-required.** Some queries phrase themselves as lookups ("find", "when is", "show me", "what's the next") but their answer requires real computation because no static page has it pre-computed. **These all get compute_mode="shallow", regardless of how they're phrased:**
- Next visible passes / pass times of any satellite from a location ("next Starlink over Tokyo at 9pm", "ISS pass times for Berlin tonight")
- Current sky position of an astronomical body from an observer ("where is Jupiter from London right now")
- Conjunction / close-approach probabilities between orbital objects ("probability of Starlink/ISS within 5km in 14 days")
- Orbital decay, atmospheric drag, re-entry predictions
- When astronomical events are visible from a location (eclipses, transits, meteor showers, ISS passes)
- Asteroid risk rankings (Palermo Scale, Torino Scale) for current PHAs
- Sunrise/sunset/twilight at arbitrary lat/lon and date — only city-level "next sunrise in Tokyo" is a lookup; arbitrary coordinates require computation
- Currency conversions over historical date ranges (ratesheet × per-row arithmetic)
- Statistical analysis or ranking across more than ~10 items where the key requires computing

These all need a library (sgp4, skyfield, astropy, ephem, pandas) or a propagation/integration step. The fact that a website exists for it doesn't mean the data is fetchable — most "trackers" are JS widgets that compute on click. Don't assume "Heavens-Above has the page." Set compute_mode="shallow" so the planner fetches raw source data (TLE catalogs from CelesTrak, ephemeris tables, almanacs) and computes properly.

Set "deep" only when the user is asking to BUILD a new codebase — webapp, CLI tool, service, library, multi-file project from scratch ("build me a todo app", "scaffold a Vue project").

Set "" for everything else, including:
- Pure information retrieval ("what is X", "current ISS altitude" — a static number from a press release)
- Summarisation / qualitative analysis ("summarise this article", "what's the gist of these results")
- Simple math the aggregator can do (one sum, one percentage, ranking <10 items by an obvious key)
- Conversational / advisory answers
- Generic web searches, file reads, system info

When unsure between "" and "shallow", prefer "shallow" for anything astronomical, orbital, financial, or statistical — the cost of a wasted coder call is much less than the cost of returning "use an external app" to the user, which is forbidden.

The presence of an existing project in the workspace is NOT a signal. Only the user's current query + prior context drive this choice. A user asking "what's the weather" after previously building a webapp still gets compute_mode="".

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
	ComputeMode        string   `json:"compute_mode"`
}

const routeSystemPrompt = `Classify ONLY the user's latest message into a handling mode, using the tool.

- "chat": pure social messages with zero actionable content — greetings, thanks, farewells, trivial acknowledgements ("hello", "hey", "thanks", "ok", "got it", "bye"). NOTHING else.
- "meta": questions about your own capabilities ("what can you do", "what tools do you have", "how do you work").
- "investigate": EVERYTHING ELSE — any task, question, complaint, imperative, follow-up, hypothetical, or expressed desire (explicit or implied). If the user names a task, output, tool, file, website, number, or person, it is investigate. When in doubt, ALWAYS choose investigate — misrouting a real request to chat blocks the user.`

/*
 * routeQuery is the cheap first pass: it decides the handling mode
 * (chat / meta / investigate) with a tiny prompt and NO skill manifest. Only the
 * agentic path then pays for the full classify + skill selection, so a "hello"
 * never loads the skills it won't use. Fails safe to "investigate" so a real
 * request is never misrouted to chat.
 */
func (a *Agent) routeQuery(ctx context.Context, alertID, query string) string {
	started := time.Now()
	trace := LLMTrace{AlertID: alertID, NodeType: "preflight", Tag: "route", Started: started, System: routeSystemPrompt, User: query}
	resp, err := a.executor.Complete(ctx, &llm.ChatRequest{
		Messages:    []llm.Message{{Role: "system", Content: routeSystemPrompt}, {Role: "user", Content: query}},
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

	started := time.Now()
	trace := LLMTrace{
		AlertID:  alertID,
		NodeType: "preflight",
		Tag:      "classify",
		Started:  started,
		System:   sysPrompt,
		User:     query,
	}

	resp, err := a.executor.Complete(ctx, &llm.ChatRequest{
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
