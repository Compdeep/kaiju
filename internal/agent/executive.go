package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/gates"
	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/agent/tools"
)

/*
 * prefixAssistantHistory rewrites conversation history for the planner.
 * desc: Keeps user messages as-is. Prefixes assistant messages with
 *       "[Executive Kernel]" so the planner doesn't mistake aggregator/reflector
 *       prose for its own prior output and mimic the format.
 */
func prefixAssistantHistory(history []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(history))
	for _, m := range history {
		if m.Role == "assistant" {
			out = append(out, llm.Message{
				Role:    "assistant",
				Content: "[Executive Kernel] " + m.Content,
			})
		} else {
			out = append(out, m)
		}
	}
	return out
}

/*
 * compileToolIndex builds a compact function-signature-style tool listing.
 * desc: Produces a string like:
 *   web_search(query*, max_results) — Search the web, returns titles/URLs/snippets
 *   bash(command*) — Run any shell command or script
 * Only includes callable tools from the registry (not guidance-only skills).
 * Called once at prompt build time.
 * param: registry - the tool registry
 * param: names - tool names to include
 * return: compiled tool index string
 */
func compileToolIndex(registry *tools.Registry, names []string) string {
	var sb strings.Builder
	sb.WriteString("## Tools (* = required param)\n")
	for _, name := range names {
		skill, ok := registry.Get(name)
		if !ok {
			continue
		}
		sig := compactParamSignature(skill.Parameters())
		sb.WriteString(fmt.Sprintf("%s(%s) — %s\n", name, sig, skill.Description()))
	}
	return sb.String()
}

/*
 * compactParamSignature extracts param names from a JSON schema into a function signature.
 * desc: Parses the tool's parameter JSON schema and produces "query*, max_results"
 *       where * marks required params.
 * param: schema - raw JSON parameter schema
 * return: comma-separated parameter signature string
 */
func compactParamSignature(schema json.RawMessage) string {
	var s struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if json.Unmarshal(schema, &s) != nil || len(s.Properties) == 0 {
		return ""
	}

	reqSet := make(map[string]bool)
	for _, r := range s.Required {
		reqSet[r] = true
	}

	var parts []string
	for name := range s.Properties {
		if reqSet[name] {
			parts = append(parts, name+"*")
		} else {
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, ", ")
}

/*
 * FlexInts is a JSON type that accepts both []int and []string.
 * desc: LLMs frequently return depends_on as ["0","1"] instead of [0,1].
 *       This type handles both formats by attempting int parse first, then
 *       string conversion. Non-numeric strings are silently skipped.
 */
type FlexInts []int

/*
 * UnmarshalJSON implements custom JSON unmarshaling for FlexInts.
 * desc: Tries parsing as []int first, then []string with numeric conversion.
 *       Non-numeric strings are skipped. Defaults to nil on complete failure.
 * param: data - the raw JSON bytes.
 * return: error (always nil — gracefully handles all inputs).
 */
func (f *FlexInts) UnmarshalJSON(data []byte) error {
	// Try []int first
	var ints []int
	if err := json.Unmarshal(data, &ints); err == nil {
		*f = ints
		return nil
	}
	// Try []string and convert
	var strs []string
	if err := json.Unmarshal(data, &strs); err == nil {
		result := make([]int, 0, len(strs))
		for _, s := range strs {
			// Try parsing as int
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				// Skip non-numeric strings like "n1" — treat as no deps
				continue
			}
			result = append(result, n)
		}
		*f = result
		return nil
	}
	// Default to empty
	*f = nil
	return nil
}

/*
 * PlanStep is one entry in the planner's JSON output.
 * desc: Contains the tool name, parameters (which may embed
 *       ${step.N.field} templates referencing prior steps),
 *       index-based depends_on references, a human-readable tag, and
 *       an optional capability gap declaration.
 */
type PlanStep struct {
	Type      string         `json:"type,omitempty"` // "tool" (default) or "compute"
	Tool      string         `json:"tool"`
	Params    map[string]any `json:"params"`
	DependsOn FlexInts       `json:"depends_on"` // index-based references
	Tag       string         `json:"tag"`
	Gap       string         `json:"gap,omitempty"` // capability gap: what's needed but unavailable
}

// UnmarshalJSON tolerates LLMs that still emit a legacy `param_refs`
// field (the old DI shape) by lifting any string-typed entries into
// Params under the same key — letting the new `${step.N.field}`
// template path handle them. Object-typed entries are dropped with a
// warning since the new format requires placeholders to be strings.
func (s *PlanStep) UnmarshalJSON(data []byte) error {
	type raw struct {
		Type      string                     `json:"type,omitempty"`
		Tool      string                     `json:"tool"`
		Params    map[string]any             `json:"params"`
		ParamRefs map[string]json.RawMessage `json:"param_refs,omitempty"` // legacy — see below
		DependsOn FlexInts                   `json:"depends_on"`
		Tag       string                     `json:"tag"`
		Gap       string                     `json:"gap,omitempty"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	s.Type = r.Type
	s.Tool = r.Tool
	s.Params = r.Params
	s.DependsOn = r.DependsOn
	s.Tag = r.Tag
	s.Gap = r.Gap
	if s.Params == nil {
		s.Params = make(map[string]any)
	}
	// Legacy compatibility — if the LLM still emits param_refs, only
	// accept string values (which would be ${step.N.field} templates)
	// and lift them into params. Object-shaped entries are the wrong-
	// format error this whole refactor was meant to eliminate; warn
	// and drop them.
	for k, v := range r.ParamRefs {
		var asStr string
		if err := json.Unmarshal(v, &asStr); err == nil {
			s.Params[k] = asStr
			log.Printf("[dag] plan: lifted legacy string param_ref %q → params (use template inline next time)", k)
			continue
		}
		log.Printf("[dag] plan: dropping legacy object param_ref %q (use ${step.N.field} string templates)", k)
	}
	return nil
}

/*
 * executiveOutput wraps the planner's JSON when intent is auto-inferred.
 * desc: When intent is explicit, the planner returns just []PlanStep.
 *       When auto, it may wrap them in {"intent":"...", "steps":[...]}.
 */
type executiveOutput struct {
	Intent string     `json:"intent"`
	Steps  []PlanStep `json:"steps"`
}

/*
 * executiveSystemPrompt returns the system prompt for the initial planner LLM call.
 * desc: Builds the complete planner system prompt including role description,
 *       planning guidance from capability cards and skills, tool definitions
 *       with parameter and output schemas, IGX section, budget limits, and
 *       expanded planner rules. Only the provided tool names are included.
 * param: relevant - slice of tool names visible to the planner.
 * param: dagMode - the DAG execution mode string.
 * param: intent - the intent level string ("auto" or a specific level).
 * return: the fully composed planner system prompt.
 */
func (a *Agent) executiveSystemPrompt(ctx context.Context, graph *Graph, relevant []string, dagMode, intent, toolIndex string) string {
	var sb strings.Builder

	// Per-investigation skill cards live on the graph.
	var cards []string
	if graph != nil {
		cards = graph.ActiveCards
	}

	// SOUL.md leads — identity + persistence litany are authoritative for every
	// LLM call in the DAG. Without this prepend the planner has no exposure to
	// the "I do not give up / never advise the user to do it themselves" cluster
	// and routinely under-plans (search-and-stop instead of search-fetch-compute).
	if a.soulPrompt != "" {
		sb.WriteString(a.soulPrompt)
		sb.WriteString("\n\n")
	}

	sb.WriteString("You are the Executive Kernel of this computer. You serve a dual purpose:\n")
	sb.WriteString("(1) Assist the user with questions, research, and conversation.\n")
	sb.WriteString("(2) Plan and decompose tasks into discrete operations available below.\n")
	sb.WriteString("    Plan in waves — each wave depends on the previous via depends_on.\n\n")
	sb.WriteString("Either way, you always answer by calling `plan`, and every tool you use is a STEP inside that plan. Example — to look something up:\n")
	sb.WriteString("  `plan({\"steps\": [{\"tool\": \"web_search\", \"params\": {\"query\": \"latest SpaceX launch date\"}, \"depends_on\": [], \"tag\": \"s1\"}]})`\n")
	sb.WriteString("If you can answer straight away with no tools, plan empty steps with an `answer`:\n")
	sb.WriteString("  `plan({\"steps\": [], \"answer\": \"Paris.\"})`\n\n")
	sb.WriteString("## Wiring data between steps\n\n")
	sb.WriteString("Every input goes in `params`. Each value is one of:\n")
	sb.WriteString("- a LITERAL — a value you know now. `\"path\": \"uploads/data.csv\"`.\n")
	sb.WriteString("- a PLACEHOLDER — `${step.N.field}`, which the dispatcher replaces with field `field` from step N's output before your step runs.\n\n")
	sb.WriteString("`depends_on` is the CLOCK, not the data. It tells the scheduler to wait for step N. Whenever you write `${step.N...}` in params, also list N in `depends_on`.\n\n")
	sb.WriteString("**Validator rule:** `depends_on:[N]` with no `${step.N...}` anywhere in params is REJECTED. If you only need sequencing without data, use `bash` (or another tool) — `compute`/`edit_file` always need data wired.\n\n")
	sb.WriteString("## Placeholder syntax\n\n")
	sb.WriteString("- `${step.N}` — full result of step N.\n")
	sb.WriteString("- `${step.N.field}` — top-level field. Dot-path for nested: `${step.0.results.0.url}`.\n")
	sb.WriteString("- Embedded mid-string: `\"yt-dlp -o 'media/%(title)s.%(ext)s' '${step.0.results.0.url}'\"`.\n")
	sb.WriteString("- Bare (entire value is the placeholder): `\"${step.N.field}\"` — preserves type (string, number, object, array passed through as-is).\n\n")
	sb.WriteString("## Examples\n\n")
	sb.WriteString("Step 0 is file_read of a CSV, step 1 is compute that processes it →\n")
	sb.WriteString("  `{\"tool\":\"compute\",\"params\":{\"goal\":\"clean and rank rows\",\"mode\":\"shallow\",\"context.csv\":\"${step.0.content}\"},\"depends_on\":[0]}`\n\n")
	sb.WriteString("Step 0 is web_search, step 1 is bash needing the URL inside a command →\n")
	sb.WriteString("  `{\"tool\":\"bash\",\"params\":{\"command\":\"yt-dlp -o 'media/%(title)s.%(ext)s' '${step.0.results.0.url}'\"},\"depends_on\":[0]}`\n\n")
	sb.WriteString("## Anti-patterns\n\n")
	sb.WriteString("- `depends_on:[0]` with no `${step.0...}` placeholder anywhere → REJECTED.\n")
	sb.WriteString("- Literal placeholders like `<URL>`, `{{url}}`, `__step.0__` → not recognised.\n")
	sb.WriteString("- Nested `{step, field}` JSON objects → placeholders are STRINGS only.\n\n")
	sb.WriteString("Make good use of tools to gather real data and help the user. If no suitable tool exists, declare a gap.\n\n")

	// Inject skill Planning Guidance sections. These are authoritative —
	// if a skill says "use compute deep", the planner must follow that,
	// not substitute file_list/file_read.
	var guidance []string
	plannerHeadings := []string{"## Planning Guidance", "## RULES"}
	activeSet := make(map[string]bool, len(cards))
	for _, k := range cards {
		activeSet[k] = true
	}
	gated := len(activeSet) > 0
	for name, gs := range a.skillGuidance {
		if gated && !activeSet[name] {
			continue
		}
		body := gs.Body()
		if body == "" {
			continue
		}
		var parts []string
		for _, heading := range plannerHeadings {
			if section := Text.ExtractSection(body, heading); section != "" {
				parts = append(parts, section)
			}
		}
		if len(parts) > 0 {
			guidance = append(guidance, fmt.Sprintf("### %s\n%s", name, strings.Join(parts, "\n\n")))
		}
	}
	if len(guidance) > 0 {
		sb.WriteString("## Skill Guidance (authoritative — follow these instructions)\n\n")
		sb.WriteString("Skill guidance is authoritative. If a skill says \"use compute deep\", use compute deep. Don't inspect — build.\n\n")
		sb.WriteString(strings.Join(guidance, "\n\n"))
		sb.WriteString("\n\n")
		log.Printf("[dag] executive injected %d skill guidance sections", len(guidance))
	} else {
		log.Printf("[dag] executive no skill guidance matched (activeCards=%v, skillGuidance=%d)", cards, len(a.skillGuidance))
	}

	// IGX section
	var igxSection string
	if intent == "auto" {
		igxSection = `Intent is auto-determined. Tools exceeding intent are blocked at execution time.`
	} else {
		igxSection = fmt.Sprintf(`Intent: **%s**. Tools exceeding intent are blocked at execution time.`, intent)
	}

	sb.WriteString(fmt.Sprintf("%s\n", igxSection))
	sb.WriteString("All tool calls pass through an intent and authorization protocol that enforces safety at execution time.\n")
	if toolIndex == "" {
		toolIndex = compileToolIndex(a.registry, relevant)
	}
	sb.WriteString(toolIndex)
	sb.WriteString("\n")

	{
		// Compute Nodes section — mirrors executive.md so native mode has the
		// same compute guidance as structured mode. Without this block the
		// model has no example showing the required `goal` and `mode` fields,
		// and no "ONE compute(deep) node" rule, so it tends to plan multiple
		// compute steps and forget the params on the second one.
		if !a.cfg.DisableCoding {
		sb.WriteString("## Compute Nodes\n")
		sb.WriteString("**Compute is the right tool whenever real computation is required.** The aggregator at the end of the plan is an LLM call that can handle small math, summarisation, and qualitative synthesis — but it CANNOT propagate orbits, run sgp4 / skyfield / pandas / scipy, parse thousand-row CSVs, or compute precision floating-point values. When the question needs any of those, **add a compute step.** The Persistence litany in your system prompt is explicit: the way to answer a hard quant question is to compute it, not to recommend an external app.\n\n")
		sb.WriteString("Only omit compute when the answer truly is a lookup (a static value from a press release, a fact from Wikipedia, a short web summary). \"Looks like a lookup\" is not the same as \"is a lookup\" — *next visible Starlink passes over Tokyo tonight* is NOT pre-computed on any page; it requires sgp4 propagation. If a known web tool exists for it (Heavens-Above, in-the-sky.org), assume the tool is a JS widget that computes on click — not a fetchable answer. Fetch the underlying TLE catalogue from CelesTrak and compute it yourself.\n\n")
		sb.WriteString("**Use compute ONLY when one of these is true:**\n")
		sb.WriteString("- A library is needed to do the work — numpy, scipy, sgp4, pandas, BeautifulSoup, jq, etc. The LLM can't run code.\n")
		sb.WriteString("- Data is too large for LLM context — CSVs with thousands of rows, log files, big JSON dumps.\n")
		sb.WriteString("- Precision matters in a way the LLM is bad at — financial math, exact floating-point, date arithmetic, sgp4, statistical inference.\n")
		sb.WriteString("- The user explicitly asked for code, a script, or a deliverable file.\n")
		sb.WriteString("- The output must be a structured value feeding another tool (not prose for the user).\n\n")
		sb.WriteString("**Do NOT use compute when:**\n")
		sb.WriteString("- Simple arithmetic the aggregator can do (one sum, one percentage, ranking <10 items).\n")
		sb.WriteString("- Pure information retrieval (\"what is X\", \"current value of Y\", \"summarise this article\").\n")
		sb.WriteString("- Qualitative analysis or conversational synthesis.\n")
		sb.WriteString("- Anything where the aggregator can read the evidence and write the answer directly.\n\n")
		sb.WriteString("When in doubt, omit compute. A wrongly-omitted compute costs nothing (aggregator handles it). A wrongly-added compute wastes an LLM call and can hallucinate when its inputs aren't real.\n\n")
		sb.WriteString("**When you DO use compute:**\n")
		sb.WriteString("- Provide the GOAL, never the code. Never write code in bash params or file_write content.\n")
		sb.WriteString("- Wire every input through `${step.N.field}` placeholders from prior gathering steps. Never plan compute over inputs that don't yet exist — it will hallucinate.\n")
		sb.WriteString("- The compute architect handles ALL implementation details (dirs, deps, file gen, service start, validation). Do NOT plan these as separate bash/service steps.\n")
		sb.WriteString("- **Use tools directly when they can do the job** — yt-dlp/curl in bash for downloads, web_fetch for pages, service for daemons. compute is for writing new code, not for wrapping existing tools in scripts.\n\n")
		sb.WriteString("**Multi-wave example — a compute task that NEEDS real-world data first:**\n")
		sb.WriteString("query: \"estimate the probability that a Starlink satellite passes within 5 km of the ISS in 14 days\"\n")
		sb.WriteString("```json\n")
		sb.WriteString("[\n")
		sb.WriteString("  {\"tool\":\"web_search\",\"params\":{\"query\":\"current Starlink TLE celestrak\"},\"depends_on\":[],\"tag\":\"search_tle\"},\n")
		sb.WriteString("  {\"tool\":\"web_search\",\"params\":{\"query\":\"current solar flux F10.7\"},\"depends_on\":[],\"tag\":\"search_flux\"},\n")
		sb.WriteString("  {\"tool\":\"web_fetch\",\"params\":{\"url\":\"${step.0.results.0.url}\",\"format\":\"text\"},\"depends_on\":[0],\"tag\":\"fetch_tle\"},\n")
		sb.WriteString("  {\"tool\":\"web_fetch\",\"params\":{\"url\":\"${step.1.results.0.url}\",\"format\":\"text\"},\"depends_on\":[1],\"tag\":\"fetch_flux\"},\n")
		sb.WriteString("  {\"type\":\"compute\",\"tool\":\"compute\",\"params\":{\"goal\":\"propagate ISS+Starlink TLEs over 14 days with sgp4, apply drag from given F10.7, count close-approaches within 5km, output probability as JSON\",\"mode\":\"shallow\",\"context.tle\":\"${step.2.content}\",\"context.flux\":\"${step.3.content}\"},\"depends_on\":[2,3],\"tag\":\"compute_probability\"}\n")
		sb.WriteString("]\n")
		sb.WriteString("```\n")
		sb.WriteString("Three waves in one plan: search → fetch → compute. Every compute input is wired from real upstream content. If your plan for a quant task ends at search/fetch with no compute, you under-planned — the user's question requires actual computation that the aggregator can't do.\n\n")
		sb.WriteString("**Counter-example — a task that does NOT need compute:**\n")
		sb.WriteString("query: \"what's the current ISS altitude?\"\n")
		sb.WriteString("```json\n")
		sb.WriteString("[\n")
		sb.WriteString("  {\"tool\":\"web_search\",\"params\":{\"query\":\"current ISS altitude\"},\"depends_on\":[],\"tag\":\"search_alt\"},\n")
		sb.WriteString("  {\"tool\":\"web_fetch\",\"params\":{\"url\":\"${step.0.results.0.url}\",\"format\":\"summary\",\"focus\":\"altitude in km\"},\"depends_on\":[0],\"tag\":\"fetch_alt\"}\n")
		sb.WriteString("]\n")
		sb.WriteString("```\n")
		sb.WriteString("The aggregator reads `fetch_alt.content` and reports the altitude. No compute needed.\n\n")
		// Preflight owns the compute-depth decision. Inject it here so the
		// planner treats it as authoritative rather than re-deriving deep vs
		// shallow from workspace residue.
		if graph != nil && graph.Preflight != nil && graph.Preflight.ComputeMode != "" {
			sb.WriteString(fmt.Sprintf("**Preflight compute_mode = %q** — use this mode for every compute step. Do NOT override based on workspace contents.\n\n", graph.Preflight.ComputeMode))
		} else {
			sb.WriteString("**Preflight compute_mode is unset.** Default to direct tools, BUT use your own judgment too — preflight is a single classifier call and can miss astrodynamics / financial / statistical / ephemeris queries that *sound* like lookups. If the question requires a library (sgp4, pandas, scipy, skyfield, ephem) or precision math, plan a compute step anyway with `mode=\"shallow\"`. Recommending an external app is forbidden by your Persistence litany — adding a compute step is always cheaper than refusing the task.\n\n")
		}
		sb.WriteString("Level reference:\n")
		sb.WriteString("- **Direct tools** (bash, service, file_read, web_search): default for downloads, searches, restarts, reads.\n")
		sb.WriteString("- **file_write**: dumb byte-writer for when you already have the exact content (literal or injected via `${step.N.field}` from an upstream step).\n")
		sb.WriteString("- **edit_file**: LLM-backed edit or create of a specific file. Use whenever you know the path and want the Coder to produce the content. task_files is REQUIRED.\n")
		sb.WriteString("- **compute(shallow)**: compute a VALUE for downstream use — analytics, rankings, scores, derived constants. The Coder emits a runnable script, the script runs, stdout is captured on `.output` so downstream steps can read it via `${step.N.output}`. NOT for editing files you already know the path of — use edit_file for that.\n")
		sb.WriteString("- **compute(deep)**: new codebases (webapp, CLI tool, service, library) built from scratch. ONE deep node per build.\n\n")
		sb.WriteString("Required params:\n")
		sb.WriteString("- compute: `goal` + `mode`. If a follow-up compute step needs data from a prior step, wire it via `${step.N.field}` placeholders AND still provide `goal` and `mode` in `params`.\n")
		sb.WriteString("- edit_file: `task_files` (at least one path) + `goal`. Skip the path and the Coder will refuse to guess — the step fails.\n\n")
		sb.WriteString("**Known-path file operations — pick the right tool:**\n")
		sb.WriteString("- Need an LLM to EDIT or CREATE a file at a known path? → `edit_file` with `task_files=[\"project/...\"]`.\n")
		sb.WriteString("- Have the exact bytes already (literal or from upstream) and need them written? → `file_write` with `path` and `content`.\n")
		sb.WriteString("- Need to COMPUTE a value (not a file) for downstream steps? → `compute(shallow)`, chain its `.output`.\n")
		sb.WriteString("NEVER use the pattern `compute(shallow) → file_write` to edit a known file — that double-writes and the wiring fails when `.output` isn't produced. Use `edit_file` for edits; `compute` is for computing values, not for producing file content to be written elsewhere.\n\n")
		sb.WriteString("Example — \"build a web app with auth\":\n")
		sb.WriteString("```json\n")
		sb.WriteString("[\n")
		sb.WriteString("  {\"type\":\"compute\",\"tool\":\"compute\",\"params\":{\"goal\":\"build a Vue 3 + Express webapp with JWT auth and SQLite database\",\"mode\":\"deep\",\"query\":\"build a Vue 3 webapp with auth\"},\"depends_on\":[],\"tag\":\"build_webapp\"}\n")
		sb.WriteString("]\n")
		sb.WriteString("```\n")
		sb.WriteString("Note: ONE compute(deep) node — the architect inside decomposes into setup, coder tasks, execute/service, and validation phases. Do not split into multiple compute(deep) nodes (\"plan blueprint then plan code then plan tests\" is wrong — that all happens INSIDE the single compute call).\n\n")
		}
		sb.WriteString("## Rules\n")
		sb.WriteString("NEVER guess values you don't know. Only use names, paths, and parameters that are visible in the evidence (workspace files, blueprint, conversation). If you don't know the exact service name, file path, or port — plan a diagnostic step first (file_read, service list, bash ls) to discover it.\n")
		sb.WriteString("NEVER interpret, judge, or refuse requests.\n")
		sb.WriteString("ALWAYS write step/field references as `${step.N.field}` placeholders inside params — never invent literal values you don't know yet.\n")
		sb.WriteString("NEVER write code in bash params.\n")
		sb.WriteString("NEVER use bash for complex multi-step tasks.\n")
		sb.WriteString("NEVER use interactive commands.\n")
		sb.WriteString("Use compute (type:\"compute\") for coding, development, and analytics work that the aggregator can't do — see the Compute Nodes decision rules above. Provide the GOAL, not the code.\n")
		sb.WriteString("ALWAYS use the compute mode preflight selected — see the Compute Nodes section above. Workspace contents do NOT influence this choice.\n")
		sb.WriteString("ALWAYS use the service tool for long-running processes (servers, daemons, dev servers, watchers, listeners). NEVER use bash for foreground servers — bash blocks the investigation waiting for the command to exit, which servers never do. service(action=\"start\", name=\"...\", command=\"...\", port=NNNN) spawns in the background and returns immediately. ALWAYS include the port parameter so health checks know which port to verify.\n")
		sb.WriteString("ALWAYS use bash only for commands that terminate: ls, grep, git, npm install, curl, node script.js, etc.\n")
		sb.WriteString("ALWAYS use web_search for questions needing current data.\n")
		sb.WriteString("ALWAYS gather required data before grafting compute. Every compute node must have its inputs supplied by prior gathering steps (web_search/web_fetch/file_read/bash) wired via `${step.N.field}` placeholders — never compute over inputs that don't exist yet.\n")
		sb.WriteString("ALWAYS plan and complete the full action the user asked for. A worklog showing prior failures is NOT a reason to plan a timid probe (e.g. just file_read) instead of the real edit/build — the user is asking again, plan the action. Imperatives ('do it', 'fix it', 'just do it') force action; never stop partway or ask for permission.\n")
		sb.WriteString("NEVER plan when context is genuinely incomplete — emit `[{\"tool\":\"gap\",\"gap\":\"<question>\"}]` only for unresolvable references, ambiguous key terms, or information only the user has. Not an escape hatch for tool choice or gatherable data.\n")
		sb.WriteString("ALWAYS build functional products that work end-to-end. If building a webapp or UI, deliver a complete, clean, working experience — not a skeleton with TODO comments.\n")
		sb.WriteString("ALWAYS include a final verification step that proves the goal has been achieved. For services: curl/http check that it responds. For scripts: run on sample input and check output. For data pipelines: run test data through and verify result shape. Never end a plan without verification — 'wrote the files' is not achievement.\n")
		if !a.cfg.DisableCoding {
		sb.WriteString("\n## Workspace Layout\n")
		sb.WriteString("- project/ — source code, application files\n")
		sb.WriteString("- media/ — downloaded media (images, videos, audio). ALWAYS save downloads here: yt-dlp -o 'media/%(title)s.%(ext)s', curl -o media/file.jpg, etc.\n")
		sb.WriteString("- blueprints/ — architecture blueprints (auto-managed by compute)\n")
		sb.WriteString("- canvas/ — user-facing visual content\n")
		sb.WriteString("- uploads/<session-id>/ — user-uploaded attachments. When the query has an [attached files] preamble, the paths land here. Each file may have <name>.meta.json (preview) and <name>.summary.md (LLM summary) sidecars.\n")
		// Workspace tree for orientation (what files exist in the workspace).
		// We do NOT inject existing blueprints here — the mere presence of
		// a blueprint in the workspace must not bias the planner toward
		// compute(deep). Whether this query needs architecture is the
		// preflight's call based on the user's actual request, not workspace
		// residue from prior runs.
		if graph != nil && graph.Context != nil {
			gateResp, gerr := graph.Context.Get(ctx, ContextRequest{
				// Depth 5 shows typical nested projects (e.g. workdir/project/<app>/<component>/src/<file>)
				// up front so the Executive doesn't have to probe for entrypoints. The
				// scanWorkspaceTree cap at 120 entries (round-robin across buckets) prevents
				// runaway on monorepo-sized trees; MaxBudget:10000 is the secondary byte cap.
				ReturnSources: Sources(WorkspaceTree(5)),
				MaxBudget:     10000,
			})
			if gerr != nil {
				log.Printf("[dag] executive context build failed: %v", gerr)
			} else if tree := gateResp.Sources[SourceWorkspaceTree]; tree != "" {
				sb.WriteString("\n### Current files\n")
				sb.WriteString(tree)
				sb.WriteString("\n")
			}
		}

		}

		sb.WriteString(fmt.Sprintf("\nBudget: max %d steps, %d LLM calls.\n", a.cfg.MaxNodes, a.cfg.MaxLLMCalls))
	}

	// Preflight hints: required tool categories the plan MUST include.
	// Populated by the pre-plan preflight call in scheduler.go.
	if graph != nil && graph.Preflight != nil && len(graph.Preflight.RequiredCategories) > 0 {
		sb.WriteString("\n## Required Tool Categories\n")
		sb.WriteString(fmt.Sprintf("This query needs tools from: %s. Your plan MUST include at least one tool from each of these categories. If none exist, declare a gap.\n",
			strings.Join(graph.Preflight.RequiredCategories, ", ")))
		sb.WriteString("Category → common tools:\n")
		sb.WriteString("- network: web_fetch, web_search\n")
		sb.WriteString("- filesystem: file_read, file_write, file_list\n")
		sb.WriteString("- compute: compute\n")
		sb.WriteString("- process: process_list, process_kill, bash\n")
		sb.WriteString("- info: sysinfo, env_list, disk_usage, net_info\n\n")
	}

	rolePrompt := sb.String() + a.fleetSection()
	return rolePrompt
}

/*
 * PlanResult contains the planner output: steps and optionally an inferred intent.
 * desc: Wraps the parsed plan steps, declared capability gaps, inferred intent
 *       level, and whether intent was auto-inferred.
 */
type PlanResult struct {
	Steps          []PlanStep
	Gaps           []string     // capability gaps declared by the planner
	InferredIntent gates.Intent // only set when intent was auto-inferred
	WasAuto        bool         // true if the planner inferred intent
}

/*
 * ExecutiveConversationalError is returned when the planner responds with
 * conversational text instead of a JSON plan.
 * desc: The Text field contains the planner's response which can be returned
 *       directly to the user as a chat response.
 */
type ExecutiveConversationalError struct {
	Text string
}

/*
 * Error returns the error message for ExecutiveConversationalError.
 * desc: Implements the error interface.
 * return: fixed error string.
 */
func (e *ExecutiveConversationalError) Error() string {
	return "planner returned conversational text instead of JSON plan"
}

/*
 * runExecutive makes a single LLM call to produce the initial investigation plan.
 * desc: When the trigger intent is Auto, the planner also infers the appropriate
 *       intent. Filters relevant skills, builds the planner prompt, sends the
 *       LLM call (with optional retry on prose output), then validates, filters,
 *       and deduplicates the resulting steps.
 * param: ctx - context for the LLM call.
 * param: trigger - the investigation trigger.
 * return: PlanResult pointer with steps and intent, or error.
 */
/*
 * runExecutive runs the planner via native function calling.
 * desc: The planner returns a PlanResult with steps and intent. Native mode
 *       is the only mode — modern LLMs all support native tool calling and
 *       it's more reliable than parsing JSON from text.
 * param: ctx - context for the LLM call.
 * param: trigger - the investigation trigger.
 * return: PlanResult pointer with steps and intent, or error.
 */
func (a *Agent) runExecutive(ctx context.Context, trigger Trigger, graph *Graph, replanFrame ...string) (*PlanResult, error) {
	return a.runExecutiveNative(ctx, trigger, graph, replanFrame...)
}

// ── Plan tool schema for native function calling mode ──────────────────────

// executiveToolSchemaTemplate is the plan meta-tool schema with a %s placeholder
// where the intent enum goes. The enum is built at call time from the
// registry so custom intent names (admin-created via the UI) show up as
// valid values to the model.
var executiveToolSchemaTemplate = `{
	"type": "object",
	"properties": {
		"answer": {
			"type": "string",
			"description": "Direct answer for trivial questions that need no tools. Only set when steps is empty."
		},
		"intent": {
			"type": "string",
			"enum": %s,
			"description": "Inferred intent level for this plan"
		},
		"steps": {
			"type": "array",
			"items": {
				"type": "object",
				"required": ["tool", "params", "depends_on", "tag"],
				"properties": {
					"type":       {"type": "string", "enum": ["tool","compute"], "description": "Node type: tool (default) or compute (LLM code generation)"},
					"tool":       {"type": "string", "description": "Tool name from the Tools list"},
					"params":     {"type": "object", "description": "Tool input params as key-value pairs. ALWAYS populate for tools with required params marked *. Use ${step.N.field} placeholders inside string values to inject results from upstream steps. Example: for web_search use {\"query\": \"search terms\"}, for web_fetch chained off step 0 use {\"url\": \"${step.0.results.0.url}\"}, for bash use {\"command\": \"ls -la\"}. NEVER leave empty."},
					"depends_on": {"type": "array", "items": {"type": "integer"}, "description": "Step indices that must complete first"},
					"tag":        {"type": "string", "description": "Short human-readable label"},
					"gap": {"type": "string", "description": "For tool=gap only: describes a missing capability"}
				}
			}
		}
	},
	"required": ["steps"]
}`

/*
 * executiveToolDef returns the meta-tool definition for native function calling mode.
 * desc: Defines a single "plan" tool whose input schema matches the PlanStep array format.
 *       The model "calls" this tool with the entire DAG as its argument. The intent
 *       enum is built at call time from the registry so admin-created custom intents
 *       are presented as valid values to the model.
 * return: llm.ToolDef for the plan meta-tool.
 */
func (a *Agent) executiveToolDef() llm.ToolDef {
	// Build the intent enum dynamically from the registry. If the registry
	// hasn't been loaded the enum is omitted entirely — Go has no knowledge
	// of specific intent names to fall back on.
	var names []string
	if a.intentRegistry != nil {
		names = a.intentRegistry.AllowedNames(-1)
	}
	enumJSON, _ := json.Marshal(names)
	schema := json.RawMessage(fmt.Sprintf(executiveToolSchemaTemplate, string(enumJSON)))
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "plan",
			Description: `Submit an execution plan. Example: {"steps":[{"tool":"web_search","params":{"query":"bitcoin price"},"depends_on":[],"tag":"s1"}]}. Chaining example: {"steps":[{"tool":"web_search","params":{"query":"news"},"depends_on":[],"tag":"s1"},{"tool":"web_fetch","params":{"url":"${step.0.results.0.url}"},"depends_on":[0],"tag":"s2"}]}. Trivial: {"steps":[],"answer":"Paris."}`,
			Parameters:  schema,
		},
	}
}

/*
 * executiveCallPayload is the parsed argument from a native plan() tool call.
 * desc: Matches the planToolSchema — contains optional intent and the steps array.
 */
type executiveCallPayload struct {
	Intent string     `json:"intent"`
	Answer string     `json:"answer"`
	Steps  []PlanStep `json:"steps"`
}

// parseExecutivePayload handles LLMs returning steps as a JSON string instead of an array.
func parseExecutivePayload(raw string, payload *executiveCallPayload) error {
	if err := json.Unmarshal([]byte(raw), payload); err != nil {
		// Try parsing with steps as a string (double-encoded JSON)
		var flex struct {
			Intent string          `json:"intent"`
			Answer string          `json:"answer"`
			Steps  json.RawMessage `json:"steps"`
		}
		if err2 := json.Unmarshal([]byte(raw), &flex); err2 != nil {
			return err
		}
		payload.Intent = flex.Intent
		payload.Answer = flex.Answer
		// Try unwrapping string-encoded steps
		var stepsStr string
		if json.Unmarshal(flex.Steps, &stepsStr) == nil {
			if err3 := ParseLLMJSON(stepsStr, &payload.Steps); err3 != nil {
				return fmt.Errorf("steps is a string but not valid JSON: %w", err3)
			}
			log.Printf("[dag] executive: unwrapped string-encoded steps (%d steps)", len(payload.Steps))
			return nil
		}
		return err
	}
	return nil
}

/*
 * runExecutiveNative makes a single LLM call using native function calling.
 * desc: Sends the plan meta-tool to the LLM. The model calls plan() with the
 *       entire DAG as the argument. No text parsing, no markdown fences.
 *       Falls back to text parsing if the model responds with text instead of a tool call.
 * param: ctx - context for the LLM call.
 * param: trigger - the investigation trigger.
 * return: PlanResult pointer with steps and intent, or error.
 */
func (a *Agent) runExecutiveNative(ctx context.Context, trigger Trigger, graph *Graph, replanFrame ...string) (*PlanResult, error) {
	relevant := a.relevantTools(ctx, formatTrigger(trigger), trigger.Scope)
	log.Printf("[dag] executive (native) sees %d tools: %v", len(relevant), relevant)
	if len(a.skillGuidance) > 0 {
		log.Printf("[dag] executive (native) has %d guidance skills loaded", len(a.skillGuidance))
	}

	dagMode := a.cfg.DAGMode
	if trigger.DAGMode != "" {
		dagMode = trigger.DAGMode
	}
	intent := trigger.Intent().String()
	// Preflight override: same logic as structured planner.
	if trigger.Intent() == gates.IntentAuto && graph != nil && graph.Preflight != nil {
		intent = graph.Preflight.Intent.String()
		log.Printf("[dag] executive (native) intent from preflight: %s", intent)
	}

	executiveHistory := prefixAssistantHistory(trigger.History)

	// One gate call for all runtime context the planner needs: worklog
	// (system state) plus the tool index (signatures + output schemas so
	// ${step.N.field} placeholders can be wired against correct result shapes).
	userQuery := formatTrigger(trigger)
	if graph != nil && graph.Preflight != nil && graph.Preflight.Context != "" {
		userQuery += "\n\n## Context\n" + graph.Preflight.Context
	}
	// Re-plan frame (optional): the reflector chose `replan` and the scheduler
	// passed a generic frame describing the gap + what's already done. Appended
	// here — above the worklog block below — so its "worklog below" reference is
	// accurate and the user's goal stays verbatim at the top.
	if len(replanFrame) > 0 && replanFrame[0] != "" {
		userQuery += replanFrame[0]
	}
	var toolIndex string
	if graph != nil && graph.Context != nil {
		gateResp, gerr := graph.Context.Get(ctx, ContextRequest{
			ReturnSources: Sources(
				Worklog(20, "all"),
				ToolIndex(relevant),
			),
			MaxBudget: 8000,
		})
		if gerr != nil {
			log.Printf("[dag] executive (native) context build failed: %v", gerr)
		} else {
			if wl := gateResp.Sources[SourceWorklog]; wl != "" {
				userQuery += "\n\n## System State (worklog)\n```\n" + wl + "\n```"
			}
			toolIndex = gateResp.Sources[SourceToolIndex]
		}
	}
	if toolIndex == "" {
		// Fallback if the gate is unavailable (defensive; shouldn't happen
		// in a normal investigation).
		toolIndex = compileToolIndex(a.registry, relevant)
	}

	sysPromptN := a.executiveSystemPrompt(ctx, graph, relevant, dagMode, intent, toolIndex)
	messages := BuildMessagesWithHistory(sysPromptN, userQuery, executiveHistory)

	startedN := time.Now()
	resp, err := a.completeHeavy(ctx, &llm.ChatRequest{
		Messages: messages,
		Tools:    []llm.ToolDef{a.executiveToolDef()},
		// PIN the model to `plan` — not just "call some tool". A weak reasoning
		// model, seeing web_search/web_fetch named all over the guidance, otherwise
		// emits a direct tool call instead of wrapping it in a plan; that hard-fails
		// with "planner called unexpected tool" and there's no retry for it.
		ToolChoice:  llm.ForceToolChoice("plan"),
		Temperature: a.cfg.Temperature,
		MaxTokens:   a.cfg.MaxTokens,
	})

	traceN := LLMTrace{
		AlertID:  trigger.AlertID,
		NodeID:   "executive",
		NodeType: "executive_native",
		Tag:      "plan",
		Started:  startedN,
		Input: map[string]string{
			"dag_mode": dagMode,
			"intent":   intent,
		},
		System:    sysPromptN,
		User:      userQuery,
		LatencyMS: time.Since(startedN).Milliseconds(),
	}

	if err != nil {
		traceN.Err = err.Error()
		WriteLLMTrace(traceN)
		return nil, fmt.Errorf("planner LLM call (native): %w", err)
	}

	if len(resp.Choices) == 0 {
		traceN.Err = "no choices"
		WriteLLMTrace(traceN)
		return nil, fmt.Errorf("planner LLM returned no choices")
	}

	// Capture output text or tool call args for the trace.
	if len(resp.Choices[0].Message.ToolCalls) > 0 {
		traceN.Output = resp.Choices[0].Message.ToolCalls[0].Function.Arguments
	} else {
		traceN.Output = resp.Choices[0].Message.Content
	}
	traceN.TokensIn = resp.Usage.PromptTokens
	traceN.TokensOut = resp.Usage.CompletionTokens
	WriteLLMTrace(traceN)

	choice := resp.Choices[0]

	// Check if the model called the plan tool
	if choice.FinishReason == "tool_calls" && len(choice.Message.ToolCalls) > 0 {
		tc := choice.Message.ToolCalls[0]
		if tc.Function.Name != "plan" {
			return nil, fmt.Errorf("planner called unexpected tool %q (expected plan)", tc.Function.Name)
		}

		log.Printf("[dag] executive (native) received plan() call, %d bytes: %s", len(tc.Function.Arguments), Text.TruncateLog(tc.Function.Arguments, 500))

		// Try parsing, with fixup for malformed compute steps
		raw := tc.Function.Arguments
		fixedRaw := fixComputeStepParams(raw)

		var payload executiveCallPayload
		if err := parseExecutivePayload(fixedRaw, &payload); err != nil {
			// Retry: send the error back and ask the planner to fix
			log.Printf("[dag] executive (native) plan() parse failed, retrying: %v", err)
			retryMessages := append(messages,
				llm.Message{Role: "assistant", Content: "", ToolCalls: choice.Message.ToolCalls},
				llm.Message{Role: "tool", ToolCallID: tc.ID, Name: "plan", Content: fmt.Sprintf("Error: %v. Fix the JSON and call plan() again. Remember: goal, mode, query go INSIDE params, not at the step level.", err)},
			)
			retryResp, retryErr := a.completeHeavy(ctx, &llm.ChatRequest{
				Messages:    retryMessages,
				Tools:       []llm.ToolDef{a.executiveToolDef()},
				ToolChoice:  llm.ForceToolChoice("plan"),
				Temperature: 0.1,
				MaxTokens:   a.cfg.MaxTokens,
			})
			if retryErr != nil {
				return nil, fmt.Errorf("parse plan() arguments (retry failed): %w", err)
			}
			if len(retryResp.Choices) > 0 && len(retryResp.Choices[0].Message.ToolCalls) > 0 {
				retryTC := retryResp.Choices[0].Message.ToolCalls[0]
				retryFixed := fixComputeStepParams(retryTC.Function.Arguments)
				log.Printf("[dag] executive (native) retry plan() call: %s", Text.TruncateLog(retryFixed, 500))
				if retryErr2 := parseExecutivePayload(retryFixed, &payload); retryErr2 != nil {
					return nil, fmt.Errorf("parse plan() arguments after retry: %w", retryErr2)
				}
			} else {
				return nil, fmt.Errorf("parse plan() arguments: %w", err)
			}
		}

		steps := payload.Steps

		// Empty steps = trivial query, planner answered directly
		if len(steps) == 0 {
			if payload.Answer != "" {
				log.Printf("[dag] executive answered directly (no tools needed): %s", Text.TruncateLog(payload.Answer, 200))
				return nil, &ExecutiveConversationalError{Text: payload.Answer}
			}
			// Empty steps with no answer — fallback to direct LLM response
			return nil, &ExecutiveConversationalError{}
		}

		// Re-plan on hallucinated tools. If the planner named a tool that isn't in
		// the registry — e.g. it called a guidance SKILL like web_research_guide as
		// if it were a tool — don't silently drop the step and collapse to a hollow
		// "starting now…" direct answer. Tell the planner exactly which names aren't
		// tools and which real tools it may use, and give it ONE chance to re-plan.
		// Mirrors the parse-retry above; if the re-plan still isn't clean, we fall
		// through to validatePlanSteps, which drops leftovers and does the old
		// conversational fallback only when nothing valid remains.
		if unknown := a.unknownToolNames(steps); len(unknown) > 0 {
			log.Printf("[dag] executive planned non-existent tool(s) %v — asking it to re-plan with real tools", unknown)
			correction := fmt.Sprintf(
				"Error: %s not callable tools — they may be skills or capabilities, not tools. "+
					"Call plan() again using ONLY real tools from this list: %s. "+
					"For researching the web, use web_search and web_fetch as steps.",
				quoteList(unknown), strings.Join(relevant, ", "))
			replanMessages := append(messages,
				llm.Message{Role: "assistant", Content: "", ToolCalls: choice.Message.ToolCalls},
				llm.Message{Role: "tool", ToolCallID: tc.ID, Name: "plan", Content: correction},
			)
			replanResp, replanErr := a.completeHeavy(ctx, &llm.ChatRequest{
				Messages:    replanMessages,
				Tools:       []llm.ToolDef{a.executiveToolDef()},
				ToolChoice:  llm.ForceToolChoice("plan"),
				Temperature: 0.1,
				MaxTokens:   a.cfg.MaxTokens,
			})
			if replanErr == nil && len(replanResp.Choices) > 0 && len(replanResp.Choices[0].Message.ToolCalls) > 0 {
				rtc := replanResp.Choices[0].Message.ToolCalls[0]
				var replanned executiveCallPayload
				if perr := parseExecutivePayload(fixComputeStepParams(rtc.Function.Arguments), &replanned); perr == nil && len(replanned.Steps) > 0 {
					log.Printf("[dag] executive re-planned after hallucination: %s", Text.TruncateLog(rtc.Function.Arguments, 500))
					steps = replanned.Steps
					payload = replanned // adopt the re-plan's intent too
				} else {
					log.Printf("[dag] executive re-plan produced no usable steps (perr=%v) — validating original", perr)
				}
			} else {
				log.Printf("[dag] executive re-plan call failed (%v) — validating original", replanErr)
			}
		}

		isAuto := trigger.Intent() == gates.IntentAuto

		// Infer intent from the payload name by resolving it through the
		// registry. Unknown names leave inferredIntent at 0 and the planner
		// falls back to tool-impact inference in validatePlanSteps.
		var inferredIntent gates.Intent
		if isAuto && payload.Intent != "" && a.intentRegistry != nil {
			if i, ok := a.intentRegistry.ByName(payload.Intent); ok {
				inferredIntent = gates.Intent(i.Rank)
			}
		}

		return a.validatePlanSteps(steps, isAuto, inferredIntent, trigger, graph.Preflight)
	}

	// Fallback: model returned text instead of a tool call.
	// Try parsing as JSON (some models ignore tool calling and write text).
	raw := choice.Message.Content
	log.Printf("[dag] executive (native) returned text instead of tool call: %s", Text.TruncateLog(raw, 200))

	if len(raw) > 0 && (raw[0] == '[' || raw[0] == '{') {
		isAuto := trigger.Intent() == gates.IntentAuto
		steps, inferredIntent, parseErr := a.parseExecutiveOutput(raw, isAuto)
		if parseErr == nil {
			return a.validatePlanSteps(steps, isAuto, inferredIntent, trigger, graph.Preflight)
		}
	}

	return nil, &ExecutiveConversationalError{Text: strings.TrimSpace(raw)}
}

/*
 * validatePlanSteps applies shared validation to parsed plan steps.
 * desc: Filters gaps, drops unknown tools, validates deps, breaks cycles,
 *       deduplicates, and infers intent if auto. Used by both structured and native planners.
 * param: steps - raw parsed plan steps.
 * param: isAuto - whether intent should be auto-inferred.
 * param: inferredIntent - pre-inferred intent from payload (or IntentObserve if not set).
 * param: trigger - the original trigger for scope checking.
 * return: validated PlanResult or error.
 */
// unknownToolNames returns the distinct step tools that don't exist in the
// registry (skipping the "gap" pseudo-tool). It's the pre-execution existence
// check: a non-empty result means the planner named something callable that
// isn't — the signal to re-plan rather than drop-and-fall-back.
func (a *Agent) unknownToolNames(steps []PlanStep) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range steps {
		if s.Tool == "" || s.Tool == "gap" || seen[s.Tool] {
			continue
		}
		if _, ok := a.registry.Get(s.Tool); !ok {
			seen[s.Tool] = true
			out = append(out, s.Tool)
		}
	}
	return out
}

// quoteList renders names as `a`, `b` is/are… for a readable correction message.
func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = "\"" + n + "\""
	}
	if len(names) == 1 {
		return q[0] + " is"
	}
	return strings.Join(q, ", ") + " are"
}

func (a *Agent) validatePlanSteps(steps []PlanStep, isAuto bool, inferredIntent gates.Intent, trigger Trigger, preflight *PreflightResult) (*PlanResult, error) {
	// Extract gaps and filter unknown tools
	var gaps []string
	valid := steps[:0]
	for _, s := range steps {
		if s.Tool == "gap" {
			if s.Gap != "" {
				gaps = append(gaps, s.Gap)
				log.Printf("[dag] executive declared gap: %s", s.Gap)
			}
			continue
		}
		if _, ok := a.registry.Get(s.Tool); ok {
			valid = append(valid, s)
		} else {
			log.Printf("[dag] executive hallucinated unknown tool %q, dropping step", s.Tool)
		}
	}
	if len(valid) == 0 && len(gaps) == 0 {
		// Every planned tool was hallucinated (none exist in the registry) and
		// there are no gaps. That means the planner found no real tools to run —
		// the same situation as an empty plan, so treat it as conversational and
		// fall back to a direct answer instead of failing the whole request.
		log.Printf("[dag] all planned tools hallucinated — falling back to conversational")
		return nil, &ExecutiveConversationalError{Text: ""}
	}

	// Data-flow validation at the executive-output boundary.
	// Catches compute / edit_file steps that declare depends_on but never
	// reference the upstream data via ${step.N.field} placeholders — a
	// common executive-LLM under-wiring bug. This is the right architectural
	// layer for the check: architect-grafted coder nodes (compute(deep)'s
	// children) communicate via files on disk and never reach this function,
	// so they're naturally exempt from a rule that doesn't apply to them.
	{
		stillValid := valid[:0]
		for _, s := range valid {
			depStrs := make([]string, len(s.DependsOn))
			for i, d := range s.DependsOn {
				depStrs[i] = fmt.Sprintf("%d", d)
			}
			if err := validateDataFlow(s.Tool, depStrs, s.Params); err != nil {
				log.Printf("[dag] executive plan validation: dropping step %q (%s): %v", s.Tag, s.Tool, err)
				continue
			}
			stillValid = append(stillValid, s)
		}
		valid = stillValid
		if len(valid) == 0 && len(gaps) == 0 {
			return nil, fmt.Errorf("planner steps all failed data-flow validation — every compute/edit_file step had depends_on without ${step.N.field} wiring")
		}
	}
	if len(valid) == 0 && len(gaps) > 0 {
		// Gaps cover two cases: missing capabilities (e.g. "I don't have an
		// SMS tool") and clarification questions (e.g. "Are you asking about
		// X or Y?"). Trust the LLM to phrase each correctly and surface it
		// verbatim — the old "Missing capabilities:" prefix was wrong for
		// clarification gaps and redundant for capability gaps.
		return nil, &ExecutiveConversationalError{
			Text: strings.Join(gaps, "\n\n"),
		}
	}

	result := &PlanResult{Steps: valid, Gaps: gaps, WasAuto: isAuto}
	if isAuto {
		// Use preflight intent as a floor — inference can raise but not lower.
		// Preflight sees the full query context; tool-impact inference only
		// sees resolved impacts which may be 0 for parametric tools (bash
		// with ${step.N.field} placeholders not yet substituted).
		preflightFloor := gates.Intent(0)
		if preflight != nil && preflight.Intent > 0 {
			preflightFloor = preflight.Intent
		}
		if inferredIntent < preflightFloor {
			inferredIntent = preflightFloor
		}
		if inferredIntent == gates.Intent(0) {
			// Resolve each tool through the intent registry so custom
			// admin-pinned intents (e.g. bash → "kill" at rank 300)
			// participate. Then snap up to the smallest registered
			// intent that covers the heaviest tool.
			maxRank := 0
			for _, s := range valid {
				skill, ok := a.registry.Get(s.Tool)
				if !ok {
					continue
				}
				rank := a.intentRegistry.ResolveToolIntent(s.Tool, skill, s.Params)
				if rank > maxRank {
					maxRank = rank
				}
			}
			inferredIntent = gates.Intent(a.intentRegistry.SnapUp(maxRank))
		}
		result.InferredIntent = inferredIntent
		log.Printf("[dag] inferred intent: %s (from plan tool impacts)", inferredIntent)
	}

	return result, nil
}

/*
 * parseExecutiveOutput extracts PlanSteps from the LLM's raw text output.
 * desc: Always expects a JSON array of steps. If the LLM wraps it in an object
 *       (e.g. {"intent":"...", "steps":[...]}), extracts the array from it.
 *       Validates deps and ${step.N} placeholder ranges, auto-adds missing dep
 *       edges, detects and breaks cycles, and deduplicates wired steps.
 * param: raw - the raw LLM output string.
 * param: isAuto - true if intent should be extracted from the response.
 * return: parsed PlanStep slice, inferred intent, or error.
 */
func (a *Agent) parseExecutiveOutput(raw string, isAuto bool) ([]PlanStep, gates.Intent, error) {
	inferredIntent := gates.Intent(0) // safe default

	var steps []PlanStep

	// Primary path: parse as JSON array
	if err := ParseLLMJSON(raw, &steps); err != nil {
		// Fallback: LLM may have wrapped in an object despite being told array-only
		var out executiveOutput
		if TryParseLLMJSON(raw, &out) && len(out.Steps) > 0 {
			steps = out.Steps
			if isAuto {
				// Resolve via the registry. Unknown names leave inferredIntent
				// at 0 (the safest default) and downstream tool-impact
				// inference in validatePlanSteps takes over.
				if i, ok := a.intentRegistry.ByName(out.Intent); ok {
					inferredIntent = gates.Intent(i.Rank)
				}
			}
			log.Printf("[dag] executive returned object instead of array, extracted %d steps", len(steps))
		} else {
			return nil, inferredIntent, fmt.Errorf("invalid JSON: %w", err)
		}
	}

	if len(steps) == 0 {
		// Empty plan means no tools needed — this is a conversational query.
		// Return as conversational error so the caller can handle it.
		return nil, inferredIntent, &ExecutiveConversationalError{Text: ""}
	}

	// Validate index-based deps and any ${step.N...} templates embedded
	// in params. Out-of-range step references in templates are blanked
	// out so planStepsToNodes leaves them as literal placeholders (the
	// LLM's mistake stays visible in logs rather than silently
	// resolving to the wrong dep). Self-references in depends_on are
	// removed.
	for i, s := range steps {
		if s.Tool == "" {
			return nil, inferredIntent, fmt.Errorf("step %d missing tool name", i)
		}
		validDeps := s.DependsOn[:0]
		for _, dep := range s.DependsOn {
			if dep < 0 || dep >= len(steps) {
				log.Printf("[dag] step %d depends_on index %d out of range, skipping", i, dep)
				continue
			}
			if dep == i {
				log.Printf("[dag] step %d depends on itself, removing self-reference", i)
				continue
			}
			validDeps = append(validDeps, dep)
		}
		steps[i].DependsOn = validDeps

		// Walk params for ${step.N(.path)?} placeholders. For each
		// match: validate range, ensure depends_on includes the
		// referenced step (auto-add if the LLM forgot — common
		// shortcut where the template is correct but depends_on is
		// missing).
		walkParams(steps[i].Params, func(str string) (any, bool) {
			matches := stepTemplateRe.FindAllStringSubmatch(str, -1)
			for _, m := range matches {
				idx, _ := strconv.Atoi(m[1])
				if idx < 0 || idx >= len(steps) {
					log.Printf("[dag] step %d template ${step.%d...} out of range", i, idx)
					continue
				}
				if idx == i {
					log.Printf("[dag] step %d template self-reference, skipping", i)
					continue
				}
				found := false
				for _, dep := range steps[i].DependsOn {
					if dep == idx {
						found = true
						break
					}
				}
				if !found {
					log.Printf("[dag] step %d template ${step.%d...} not in depends_on, auto-adding", i, idx)
					steps[i].DependsOn = append(steps[i].DependsOn, idx)
				}
			}
			return str, false
		})
	}

	// Cycle detection — a DAG must be acyclic. Detect and break any cycles.
	// Uses topological sort; steps involved in cycles have their offending deps removed.
	if hasCycle, fixed := breakCycles(steps); hasCycle {
		log.Printf("[dag] cycle detected in plan, removed offending dependencies")
		steps = fixed
	}

	// Dedup: drop steps that fetch the same URL via identical ${step.N.field} placeholders.
	// The planner sometimes creates two fetch phases for the same search results
	// with different focus params — one fetch with a broad focus is sufficient.
	steps = deduplicateParamRefSteps(steps)

	return steps, inferredIntent, nil
}

/*
 * breakCycles checks for cycles in the step dependency graph.
 * desc: Uses DFS with visit states (unvisited/visiting/visited). If a back
 *       edge is found, the offending dependency is removed to break the cycle.
 * param: steps - the plan steps to check.
 * return: true if any cycles were found, and the fixed steps.
 */
func breakCycles(steps []PlanStep) (bool, []PlanStep) {
	n := len(steps)
	// States: 0=unvisited, 1=visiting (in current DFS path), 2=visited
	state := make([]int, n)
	hasCycle := false

	var dfs func(i int) bool
	dfs = func(i int) bool {
		state[i] = 1
		newDeps := steps[i].DependsOn[:0]
		for _, dep := range steps[i].DependsOn {
			if dep < 0 || dep >= n {
				continue
			}
			if state[dep] == 1 {
				// Back edge — this is a cycle. Drop this dependency.
				log.Printf("[dag] breaking cycle: step %d → step %d", i, dep)
				hasCycle = true
				continue
			}
			if state[dep] == 0 {
				if dfs(dep) {
					// Cycle found deeper — deps already cleaned
				}
			}
			newDeps = append(newDeps, dep)
		}
		steps[i].DependsOn = newDeps
		state[i] = 2
		return hasCycle
	}

	for i := 0; i < n; i++ {
		if state[i] == 0 {
			dfs(i)
		}
	}
	return hasCycle, steps
}

/*
 * deduplicateParamRefSteps removes steps that have identical tool + param_ref
 * source (same step + same field).
 * desc: Catches the common case where the planner creates two fetch phases for
 *       the same search results with different focus params. Merges focus params
 *       into the first occurrence and drops the duplicate.
 * param: steps - the plan steps to deduplicate.
 * return: deduplicated steps with remapped depends_on and param_ref indices.
 */
func deduplicateParamRefSteps(steps []PlanStep) []PlanStep {
	type refKey struct {
		tool  string
		step  int
		field string
	}

	// Helper: pull the first ${step.N(.path)?} placeholder out of a
	// step's params. Used as the dedup key. Returns step=-1 if no
	// template anywhere.
	firstTemplate := func(params map[string]any) (int, string, bool) {
		var foundStep int
		var foundField string
		var ok bool
		walkParams(params, func(str string) (any, bool) {
			if ok {
				return str, false
			}
			if m := stepTemplateRe.FindStringSubmatch(str); m != nil {
				foundStep, _ = strconv.Atoi(m[1])
				foundField = m[2]
				ok = true
			}
			return str, false
		})
		return foundStep, foundField, ok
	}

	seen := make(map[refKey]int) // refKey → first step index
	dropSet := make(map[int]bool)

	for i, s := range steps {
		step, field, hasTemplate := firstTemplate(s.Params)
		if !hasTemplate {
			continue
		}
		key := refKey{tool: s.Tool, step: step, field: field}
		if firstIdx, exists := seen[key]; exists {
			// Duplicate — merge focus params if possible, drop this step
			if firstFocus, ok := steps[firstIdx].Params["focus"].(string); ok {
				if dupFocus, ok2 := s.Params["focus"].(string); ok2 && dupFocus != firstFocus {
					steps[firstIdx].Params["focus"] = firstFocus + ", " + dupFocus
				}
			}
			dropSet[i] = true
			log.Printf("[dag] dedup: step %d (%s) duplicates step %d (same %s ← step.%d.%s), merging", i, s.Tag, firstIdx, s.Tool, step, field)
		} else {
			seen[key] = i
		}
	}

	if len(dropSet) == 0 {
		return steps
	}

	// Rebuild steps without dropped entries, remapping depends_on indices
	indexMap := make(map[int]int) // old index → new index
	var result []PlanStep
	for i, s := range steps {
		if dropSet[i] {
			continue
		}
		indexMap[i] = len(result)
		result = append(result, s)
	}

	// Remap depends_on and param_ref step indices
	for i := range result {
		newDeps := result[i].DependsOn[:0]
		for _, dep := range result[i].DependsOn {
			if newIdx, ok := indexMap[dep]; ok {
				newDeps = append(newDeps, newIdx)
			}
			// If dep was dropped, skip it (its work is merged into the kept step)
		}
		result[i].DependsOn = newDeps

		// Rewrite any ${step.OLD...} placeholders inside params to point
		// at the post-dedup index. Templates referencing dropped steps
		// are remapped to the kept step (whose work absorbed theirs).
		walkParams(result[i].Params, func(s string) (any, bool) {
			out := stepTemplateRe.ReplaceAllStringFunc(s, func(match string) string {
				m := stepTemplateRe.FindStringSubmatch(match)
				old, _ := strconv.Atoi(m[1])
				newIdx, ok := indexMap[old]
				if !ok {
					return match // dropped without a survivor — leave placeholder for downstream complaint
				}
				if m[2] == "" {
					return "${step." + strconv.Itoa(newIdx) + "}"
				}
				return "${step." + strconv.Itoa(newIdx) + "." + m[2] + "}"
			})
			if out != s {
				return out, true
			}
			return s, false
		})
	}

	log.Printf("[dag] dedup removed %d duplicate steps (%d → %d)", len(dropSet), len(steps), len(result))
	return result
}

/*
 * planStepsToNodes converts parsed plan steps into graph nodes.
 * desc: Two-pass: first create all nodes (collecting IDs), then resolve index
 *       deps and rewrites ${step.N} placeholders to real node IDs. Filters duplicate tool+params
 *       against already-executed nodes (for replan grafts). Optionally injects
 *       reflection nodes between depth waves (reflect mode only).
 * param: steps - the parsed plan steps.
 * param: graph - the investigation graph.
 * param: budget - the execution budget.
 * param: registry - tool registry for source tagging and schema validation (optional).
 * param: dagMode - optional DAG mode override for reflection injection.
 * return: slice of created Node pointers, or error.
 */
func planStepsToNodes(steps []PlanStep, graph *Graph, budget *Budget, registry *tools.Registry, dagMode ...string) ([]*Node, error) {
	// Pass 1: create nodes and collect their graph IDs
	nodeIDs := make([]string, len(steps))
	nodes := make([]*Node, len(steps))

	for i, s := range steps {

		// At plan time, only check total node count — not per-tool wave limits.
		// Per-tool limits are for execution batching, not planning.
		// Pass "" for tool to skip wave counter; pass false for isLLM (tool node).
		if !budget.TrySpawnNode("", false) {
			log.Printf("[dag] budget exhausted at step %d, truncating plan", i)
			nodes = nodes[:i]
			break
		}

		// Validate tool exists — reject hallucinated tool names at graft time
		// instead of failing at execution time with "unknown tool"
		if s.Tool != "compute" && s.Tool != "" && registry != nil {
			if _, ok := registry.Get(s.Tool); !ok {
				log.Printf("[dag] dropping step %q — unknown tool %q (hallucinated)", s.Tag, s.Tool)
				continue
			}
		}

		nodeType := NodeTool
		if s.Type == "compute" || s.Tool == "compute" {
			nodeType = NodeCompute
		}
		n := &Node{
			Type:     nodeType,
			ToolName: s.Tool,
			Params:   s.Params,
			Tag:      s.Tag,
		}
		// Tag the node with its tool source for frontend display
		if registry != nil {
			n.Source = registry.GetSource(s.Tool)
		}
		id := graph.AddNode(n)
		nodeIDs[i] = id
		nodes[i] = n
	}

	// Pass 2: resolve index-based deps to node IDs, then walk each step's
	// params for ${step.N(.path)?} placeholders. Rewrite each placeholder
	// to ${node.<id>(.path)?} so the dispatcher can resolve via the graph
	// at execute time, and ensure every referenced step is in DependsOn.
	for i, s := range steps {
		if i >= len(nodes) || nodes[i] == nil {
			break // truncated by budget
		}
		for _, depIdx := range s.DependsOn {
			if depIdx < len(nodeIDs) && nodeIDs[depIdx] != "" {
				nodes[i].DependsOn = append(nodes[i].DependsOn, nodeIDs[depIdx])
			}
		}

		// Walk params, rewrite ${step.N...} → ${node.<id>...}, collect
		// implicit deps. Logs each substitution for trace visibility.
		extraDeps := rewriteStepTemplates(nodes[i].Params, nodeIDs, nodes[i].ID, steps, registry)
		for _, dep := range extraDeps {
			has := false
			for _, d := range nodes[i].DependsOn {
				if d == dep {
					has = true
					break
				}
			}
			if !has {
				nodes[i].DependsOn = append(nodes[i].DependsOn, dep)
			}
		}
	}

	// Wave reflections removed — the scheduler handles reflection timing.
	// Injecting reflections at plan time caused cascading debugger spawns
	// when early waves failed and all reflection nodes became ready at once.

	return nodes, nil
}

// stepTemplateRe matches ${step.N(.path)?} placeholders in param strings.
// Used at plan time to rewrite step indices to concrete node IDs and to
// validate field paths against upstream output schemas.
//
//	${step.0}                 → match, step=0, path=""
//	${step.3.content}         → match, step=3, path="content"
//	${step.0.results.0.url}   → match, step=0, path="results.0.url"
//	${node.X.field}           → not matched (already rewritten)
var stepTemplateRe = regexp.MustCompile(`\$\{step\.(\d+)(?:\.([^}]+))?\}`)

// rewriteStepTemplates walks every string value reachable in params,
// finds ${step.N(.path)?} placeholders, and rewrites them in place to
// ${node.<id>(.path)?} using the plan's step-index → node-id mapping.
// Returns the set of node IDs referenced by templates so the caller can
// add them to depends_on if not already present (a common LLM omission).
//
// Field-path warnings against upstream tool output schemas are logged
// here too — the same diagnostic the param_refs code path used to emit,
// just keyed off the new template syntax.
func rewriteStepTemplates(params map[string]any, nodeIDs []string, owner string, steps []PlanStep, registry *tools.Registry) []string {
	var implicitDeps []string
	walkParams(params, func(s string) (any, bool) {
		out := stepTemplateRe.ReplaceAllStringFunc(s, func(match string) string {
			m := stepTemplateRe.FindStringSubmatch(match)
			idx, _ := strconv.Atoi(m[1])
			field := m[2]
			if idx < 0 || idx >= len(nodeIDs) || nodeIDs[idx] == "" {
				log.Printf("[dag] template %s on %s references invalid step %d, leaving placeholder unresolved", match, owner, idx)
				return match
			}
			depID := nodeIDs[idx]
			implicitDeps = append(implicitDeps, depID)
			rewritten := "${node." + depID + "}"
			if field != "" {
				rewritten = "${node." + depID + "." + field + "}"
			}
			log.Printf("[dag] template %s on %s ← node %s%s", match, owner, depID, dotPrefix(field))
			// Validate field against upstream's declared output schema —
			// best-effort warning only, mirrors the legacy behaviour.
			if registry != nil && field != "" {
				upstreamTool := steps[idx].Tool
				if skill, ok := registry.Get(upstreamTool); ok {
					outSchema := tools.GetOutputSchema(skill)
					if outSchema == nil {
						log.Printf("[dag] warning: template on %s references %s which has no output schema", owner, upstreamTool)
					} else if !fieldExistsInSchema(outSchema, field) {
						log.Printf("[dag] warning: template on %s references field %q not in %s output schema", owner, field, upstreamTool)
					}
				}
			}
			return rewritten
		})
		if out != s {
			return out, true
		}
		return s, false
	})
	return implicitDeps
}

// (dotPrefix lives in dispatcher.go — same package.)

/*
 * fieldExistsInSchema checks if a dot-path field exists in a JSON Schema's properties.
 * desc: Used to validate template field paths against declared output schemas
 *       at plan time. Supports nested objects and array items traversal.
 * param: schemaJSON - the raw JSON Schema bytes.
 * param: fieldPath - dot-separated field path to validate.
 * return: true if the field path exists in the schema.
 */
func fieldExistsInSchema(schemaJSON json.RawMessage, fieldPath string) bool {
	var schema map[string]any
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return false
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return false
	}
	parts := strings.Split(fieldPath, ".")
	current := props
	for i, part := range parts {
		// Numeric index — skip this segment (we're indexing into an array
		// whose items properties are already loaded in `current`).
		if isNumericPathSegment(part) {
			continue
		}
		prop, exists := current[part]
		if !exists {
			return false
		}
		if i == len(parts)-1 {
			return true
		}
		propObj, ok := prop.(map[string]any)
		if !ok {
			return false
		}
		// Array type: descend into items.properties
		if propObj["type"] == "array" {
			items, ok := propObj["items"].(map[string]any)
			if !ok {
				return false
			}
			nested, ok := items["properties"].(map[string]any)
			if !ok {
				return false
			}
			current = nested
			continue
		}
		nested, ok := propObj["properties"].(map[string]any)
		if !ok {
			return false
		}
		current = nested
	}
	return true
}

/*
 * isNumericPathSegment returns true if the string contains only digits.
 * desc: Used by fieldExistsInSchema to detect array index segments in dot-paths.
 * param: s - the string to check.
 * return: true if s is non-empty and all characters are digits.
 */
func isNumericPathSegment(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
