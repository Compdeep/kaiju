package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/prompt"
	"github.com/Compdeep/kaiju/agent/skillmd"
	"github.com/Compdeep/kaiju/agent/toolapi"
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
func compileToolIndex(registry *toolapi.Registry, names []string) string {
	var sb strings.Builder
	sb.WriteString("## Tools (* = required param)\n")
	sb.WriteString("The parameters shown for each tool are the only ones available — use only the parameters listed.\n")
	for _, name := range names {
		sb.WriteString(toolIndexEntry(registry, name))
	}
	return sb.String()
}

// toolIndexEntry is one tool's lines of the index: its signature and
// description, then the shape it returns and the wiring it declared.
//
// Split out of compileToolIndex so the size of an entry can be asked for
// without assembling the whole index — relevantTools has to know what a tool
// costs before it decides to show it. Returns "" for a name the registry does
// not hold, so an entry always costs what it is worth.
func toolIndexEntry(registry *toolapi.Registry, name string) string {
	tool, ok := registry.Get(name)
	if !ok {
		return ""
	}
	var sb strings.Builder
	sig := compactParamSignature(tool.Parameters())
	sb.WriteString(fmt.Sprintf("%s(%s) — %s\n", name, sig, tool.Description()))
	if outSchema := toolapi.GetOutputSchema(tool); outSchema != nil {
		if shape := compactOutputShape(outSchema); shape != "" {
			sb.WriteString("  → returns: " + shape + "\n")
		}
		// Wiring the tool itself declared. Generated rather than written
		// into a description, so it cannot drift from what the references read
		// afterwards to check whether the wiring happened.
		for _, hint := range chainHints(outSchema) {
			sb.WriteString("  → chain: " + hint + "\n")
		}
	}
	return sb.String()
}

// toolIndexEntrySize is what showing one tool to the planner costs, in
// characters of its index.
func toolIndexEntrySize(registry *toolapi.Registry, name string) int {
	return len(toolIndexEntry(registry, name))
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
 *       index-based depends_on references, and a human-readable tag.
 */
type PlanStep struct {
	Type      string         `json:"type,omitempty"` // "tool" (default) or "compute"
	Tool      string         `json:"tool"`
	Params    map[string]any `json:"params"`
	DependsOn FlexInts       `json:"depends_on"` // index-based references
	Tag       string         `json:"tag"`
	// Target names the machine this step runs on. Usually left to
	// applyRunTarget rather than written by the planner, since a tool's own
	// declaration says whether it needs one.
	Target string `json:"target,omitempty"`
	// UnresolvedDeps holds depends_on entries that named neither a position nor
	// any step's tag. Kept rather than discarded so the plan can be sent back
	// for correction: a step declaring a dependency on something that is not in
	// the plan is a broken plan, and the executive can fix one it is told about.
	UnresolvedDeps []string `json:"-"`
}

// planStepFields is every JSON name a step may carry: PlanStep's own, read off
// its tags, plus the legacy param_refs this decoder still accepts.
//
// Read rather than listed. A hand-written list is a third place that has to
// agree with the struct, and the point of this decoder is that it no longer has
// a second.
var planStepFields = func() map[string]bool {
	names := map[string]bool{"param_refs": true}
	t := reflect.TypeOf(PlanStep{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			names[name] = true
		}
	}
	return names
}()

/*
 * UnmarshalJSON decodes one plan step.
 * desc: It decodes into PlanStep itself, through a local type that has the same
 *       fields and none of its methods — which is what stops the decoder
 *       calling itself. It used to declare a second struct listing every field
 *       by hand and copy them across one at a time, and a field added to
 *       PlanStep and not to that copy was dropped with no error. Target is how
 *       that was found: a plan named the machine to run on and the step arrived
 *       without one.
 *
 *       It also tolerates a model that still emits the legacy param_refs field,
 *       lifting string entries into Params under the same key so the
 *       ${step.N.field} path handles them. Object entries are the wrong-format
 *       error that path replaced, so they are named and dropped.
 * param: data - the step as the model wrote it.
 * return: an error only when the step itself will not decode.
 */
func (s *PlanStep) UnmarshalJSON(data []byte) error {
	// params arrives one of two ways and both are decoded here.
	//
	// As an object, which is what a tool call carries and what Anthropic still
	// sends. And as a STRING holding that object, which is what the schema now
	// declares — a step's params is a map whose keys are whatever the named
	// tool takes, and a strict decoder cannot express a map with undeclared
	// keys. Carrying it as a string is what closes the schema, at the cost of
	// the model writing one level of escaping.
	//
	// The string is unwrapped before the fields decode rather than after,
	// because the decode below would otherwise refuse it outright: a string
	// where a map is expected is an error, not an empty map.
	rest, fromString, err := unwrapStringParams(data)
	if err != nil {
		return err
	}

	// Same fields, no methods, so json does not re-enter this one.
	type planStep PlanStep
	var step planStep
	if err := json.Unmarshal(rest, &step); err != nil {
		return err
	}
	*s = PlanStep(step)
	if fromString != nil {
		s.Params = fromString
	}
	if s.Params == nil {
		s.Params = make(map[string]any)
	}

	// param_refs is read separately because it is not a field of PlanStep and
	// is not becoming one — it is a shape this decoder accepts and translates,
	// not a shape a step has.
	var legacy struct {
		ParamRefs map[string]json.RawMessage `json:"param_refs,omitempty"`
	}
	if err := json.Unmarshal(data, &legacy); err == nil {
		for k, v := range legacy.ParamRefs {
			var asStr string
			if err := json.Unmarshal(v, &asStr); err == nil {
				s.Params[k] = asStr
				log.Printf("[dag] plan: lifted legacy string param_ref %q → params (use template inline next time)", k)
				continue
			}
			log.Printf("[dag] plan: dropping legacy object param_ref %q (use ${step.N.field} string templates)", k)
		}
	}

	// Anything else the model invented is dropped by the decode above, and a
	// silent drop in plan parsing is how a step loses its wiring and still
	// runs. Name what went missing.
	var seen map[string]json.RawMessage
	if err := json.Unmarshal(data, &seen); err != nil {
		return nil // the step decoded; a second parse failing changes nothing
	}
	for key := range seen {
		if !planStepFields[key] {
			log.Printf("[plan:warn] step %q (tag=%q) emitted unknown field %q — dropped",
				s.Tool, s.Tag, key)
		}
	}
	return nil
}

/*
 * unwrapStringParams separates a params written as a string from the rest of
 * the step.
 * desc: Returns the step with params removed when it was a string, so the
 *       field decode that follows does not meet a string where it wants a map,
 *       and the decoded object beside it.
 *
 *       An object params is left exactly where it was and nil is returned for
 *       the second value, so the ordinary path is untouched — including the
 *       bytes, since the other fields travel as RawMessage and are never
 *       re-encoded.
 *
 *       An empty string means no parameters, not a broken step: a model told
 *       to write "{}" sometimes writes "". Anything else that will not parse is
 *       an error naming what was wrong, which is what the plan retry sends back
 *       to the planner.
 * param: data - the step as the model wrote it.
 * return: the step to decode, the params when they came as a string, and an
 *         error when that string held no readable object.
 */
func unwrapStringParams(data []byte) ([]byte, map[string]any, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return data, nil, nil // not an object; the decode below reports it
	}
	rawParams, present := fields["params"]
	if !present {
		return data, nil, nil
	}
	var asString string
	if err := json.Unmarshal(rawParams, &asString); err != nil {
		return data, nil, nil // an object, as it has always been
	}

	params := map[string]any{}
	if trimmed := strings.TrimSpace(asString); trimmed != "" {
		if err := ParseLLMJSON(trimmed, &params); err != nil {
			return nil, nil, fmt.Errorf("params must be a JSON object written as a string, "+
				"like \"{\\\"path\\\": \\\"/tmp/x\\\"}\" — this one does not parse: %w", err)
		}
	}

	delete(fields, "params")
	rest, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, err
	}
	return rest, params, nil
}

/*
 * objectFromRaw reads a JSON object that may have arrived wrapped in a string.
 * desc: The companion to unwrapStringParams, for a field that is a whole value
 *       rather than one entry of a step. Same reason: an object whose keys are
 *       the model's own invention cannot be described to a strict decoder, so
 *       the field is declared a string and the object travels inside it.
 *
 *       Both forms are read. The object form is what a tool call carries, what
 *       Anthropic sends, and what every file already written in the old shape
 *       holds — a session resumed after this change reads its own earlier
 *       contracts back.
 *
 *       An empty string is an empty object, not a fault: it is how a field that
 *       strict has made mandatory says it has nothing.
 * param: raw - the field as it arrived.
 * return: the object, and an error when the value held no readable one.
 */
func objectFromRaw(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		trimmed := strings.TrimSpace(asString)
		if trimmed == "" {
			return nil, nil
		}
		var out map[string]any
		if err := ParseLLMJSON(trimmed, &out); err != nil {
			return nil, fmt.Errorf("written as a string, but it holds no JSON object: %w", err)
		}
		return out, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
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
 *       planning guidance from skill card and skills, tool definitions
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

	// Planning contract (identity + dependency-injection rules). Lives in the
	// composable prompt package (prompt.Executive, section EXECUTIVE in
	// prompts.md) so it is operator-overridable like every other role prompt.
	sb.WriteString(prompt.Executive)
	sb.WriteString("\n\n")

	// What this query must reach for, before anything it may reach with.
	//
	// Preflight decides this and it is the one line in the prompt that binds:
	// a plan that names no network tool for a question about the present is
	// wrong, and this says so. It used to be written last — after the tool
	// index, the compute doctrine, the rules and a workspace listing, at the
	// end of a document of nearly sixty thousand characters. A constraint
	// nobody reaches is a constraint nobody applies.
	if graph != nil && graph.Preflight != nil && len(graph.Preflight.RequiredCategories) > 0 {
		sb.WriteString("## Required Tool Categories\n")
		sb.WriteString(fmt.Sprintf("This query needs tools from: %s. Your plan MUST include at least one tool from each of these categories.\n",
			strings.Join(graph.Preflight.RequiredCategories, ", ")))
		sb.WriteString("Category → common tools:\n")
		sb.WriteString("- network: web_fetch, web_search\n")
		sb.WriteString("- filesystem: file_read, file_write, file_list\n")
		sb.WriteString("- compute: compute\n")
		sb.WriteString("- process: process_list, process_kill, bash\n")
		sb.WriteString("- info: sysinfo, env_list, disk_usage, net_info\n\n")
	}

	// The identity and the persistence litany, AFTER the planning contract
	// rather than before it.
	//
	// It stays because removing it made the planner under-plan — search and
	// stop, where the litany is what says keep going. But it opens "you are a
	// conversational assistant first… tools are a means, never the point",
	// which is true of the stage that writes the answer and false of this one.
	// Read first it framed everything after it; read here it qualifies a
	// contract already given.
	if a.soulPrompt != "" {
		sb.WriteString(a.soulPrompt)
		sb.WriteString("\n\n")
	}

	// Inject skill Planning Guidance sections. These are authoritative —
	// if a skill says "use compute deep", the planner must follow that,
	// not substitute file_list/file_read.
	var guidance []string
	plannerHeadings := skillmd.PlannerHeadings
	// The keys this run selected, or every key when it selected none — the
	// planner has always fallen back to all of them rather than to nothing.
	//
	// Resolved through lookupGuidanceBody, so a skill card counts as well
	// as a SKILL.md. This loop read one of the two stores, which meant every
	// "## Planning Guidance" section in a skill card was written for a
	// planner that never saw it. Iterating the keys rather than a map also
	// fixes the order, which varied between runs.
	keys := cards
	if len(keys) == 0 {
		keys = a.guidanceKeys()
	}
	for _, key := range keys {
		body, name := a.lookupGuidanceBody(key)
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
		// Included when this run can actually call compute, not when a setting
		// says coding is on. The list this prompt is built from is already
		// filtered by reach and by the run's scope, so asking it is asking the
		// same question the dispatcher will ask later.
		if slices.Contains(relevant, computeToolName) {
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
			sb.WriteString("- Wire every input through a reference to the step that produced it — `${step.<tag>.<path>}`. Never plan compute over inputs that don't yet exist — it will hallucinate.\n")
			sb.WriteString("- The compute architect handles ALL implementation details (dirs, deps, file gen, service start, validation). Do NOT plan these as separate bash/service steps.\n")
			sb.WriteString("- **Use tools directly when they can do the job** — yt-dlp/curl in bash for downloads, web_fetch for pages, service for daemons. compute is for writing new code, not for wrapping existing tools in scripts.\n\n")
			sb.WriteString("**Multi-batch example — a compute task that NEEDS real-world data first:**\n")
			sb.WriteString("query: \"estimate the probability that a Starlink satellite passes within 5 km of the ISS in 14 days\"\n")
			sb.WriteString("```json\n")
			sb.WriteString("[\n")
			sb.WriteString("  {\"tool\":\"web_search\",\"params\":\"{\\\"query\\\":\\\"current Starlink TLE celestrak\\\"}\",\"tag\":\"search_tle\"},\n")
			sb.WriteString("  {\"tool\":\"web_search\",\"params\":\"{\\\"query\\\":\\\"current solar flux F10.7\\\"}\",\"tag\":\"search_flux\"},\n")
			sb.WriteString("  {\"tool\":\"web_fetch\",\"params\":\"{\\\"url\\\":\\\"${step.search_tle.results.0.url}\\\",\\\"format\\\":\\\"text\\\"}\",\"tag\":\"fetch_tle\"},\n")
			sb.WriteString("  {\"tool\":\"web_fetch\",\"params\":\"{\\\"url\\\":\\\"${step.search_flux.results.0.url}\\\",\\\"format\\\":\\\"text\\\"}\",\"tag\":\"fetch_flux\"},\n")
			sb.WriteString("  {\"type\":\"compute\",\"tool\":\"compute\",\"params\":\"{\\\"goal\\\":\\\"propagate ISS+Starlink TLEs over 14 days with sgp4, apply drag from given F10.7, count close-approaches within 5km, output probability as JSON\\\",\\\"mode\\\":\\\"shallow\\\",\\\"context.tle\\\":\\\"${step.fetch_tle.content}\\\",\\\"context.flux\\\":\\\"${step.fetch_flux.content}\\\"}\",\"tag\":\"compute_probability\"}\n")
			sb.WriteString("]\n")
			sb.WriteString("```\n")
			sb.WriteString("Three batches in one plan: search → fetch → compute. Every compute input is wired from real upstream content. If your plan for a quant task ends at search/fetch with no compute, you under-planned — the user's question requires actual computation that the aggregator can't do.\n\n")
			sb.WriteString("**Counter-example — a task that does NOT need compute:**\n")
			sb.WriteString("query: \"what's the current ISS altitude?\"\n")
			sb.WriteString("```json\n")
			sb.WriteString("[\n")
			sb.WriteString("  {\"tool\":\"web_search\",\"params\":\"{\\\"query\\\":\\\"current ISS altitude\\\"}\",\"tag\":\"search_alt\"},\n")
			sb.WriteString("  {\"tool\":\"web_fetch\",\"params\":\"{\\\"url\\\":\\\"${step.search_alt.results.0.url}\\\",\\\"format\\\":\\\"summary\\\",\\\"focus\\\":\\\"altitude in km\\\"}\",\"tag\":\"fetch_alt\"}\n")
			sb.WriteString("]\n")
			sb.WriteString("```\n")
			sb.WriteString("The aggregator reads `fetch_alt.content` and reports the altitude. No compute needed.\n\n")
			// Preflight owns the compute-depth decision. Inject it here so the
			// planner treats it as authoritative rather than re-deriving deep vs
			// shallow from workspace residue.
			if graph != nil && graph.Preflight != nil && graph.Preflight.ComputeMode != "" {
				sb.WriteString(fmt.Sprintf("**Preflight compute_mode = %q** — use this mode for every compute step. Do NOT override based on workspace contents.\n\n", graph.Preflight.ComputeMode))
			} else {
				sb.WriteString("**Preflight compute_mode is unset.** Default to direct tools, BUT use your own judgment too — preflight is a single classifier call and can miss astrodynamics / financial / statistical / ephemeris queries that *sound* like lookups. If the question requires a library (sgp4, pandas, scipy, skyfield, ephem) or precision math, plan a compute step anyway with `mode=\"shallow\"`. Recommending an external app is forbidden by your Persistence litany — where a script could produce the answer, a compute step costs far less than refusing the task. Where one could not, it costs more than doing nothing: compute writes a program and runs it, and a program cannot tell you what a page says. Reading sources and reporting what they hold is the aggregator's work, and it already sees every result this run gathered.\n\n")
			}
			sb.WriteString("Level reference:\n")
			sb.WriteString("- **Direct tools** (bash, service, file_read): default for downloads, searches, restarts, reads.\n")
			sb.WriteString("- **file_write**: dumb byte-writer for when you already have the exact content (literal or wired from an upstream step with `${step.<tag>.<path>}`).\n")
			sb.WriteString("- **edit_file**: LLM-backed edit or create of a specific file. Use whenever you know the path and want the Coder to produce the content. task_files is REQUIRED.\n")
			sb.WriteString("- **compute(shallow)**: compute a VALUE for downstream use — analytics, rankings, scores, derived constants. The Coder emits a runnable script, the script runs, stdout is captured on `.output` so downstream steps can read it with `${step.<tag>.output}`. NOT for editing files you already know the path of — use edit_file for that.\n")
			sb.WriteString("- **compute(deep)**: new codebases (webapp, CLI tool, service, library) built from scratch. ONE deep node per build.\n\n")
			sb.WriteString("Required params:\n")
			sb.WriteString("- compute: `goal` + `mode`. If a follow-up compute step needs data from a prior step, wire it with `${step.<tag>.<path>}` AND still provide `goal` and `mode` in `params`.\n")
			sb.WriteString("- edit_file: `task_files` (at least one path) + `goal`. Skip the path and the Coder will refuse to guess — the step fails.\n\n")
			sb.WriteString("**Known-path file operations — pick the right tool:**\n")
			sb.WriteString("- Need an LLM to EDIT or CREATE a file at a known path? → `edit_file` with `task_files=[\"project/...\"]`.\n")
			sb.WriteString("- Have the exact bytes already (literal or from upstream) and need them written? → `file_write` with `path` and `content`.\n")
			sb.WriteString("- Need to COMPUTE a value (not a file) for downstream steps? → `compute(shallow)`, chain its `.output`.\n")
			sb.WriteString("NEVER use the pattern `compute(shallow) → file_write` to edit a known file — that double-writes and the wiring fails when `.output` isn't produced. Use `edit_file` for edits; `compute` is for computing values, not for producing file content to be written elsewhere.\n\n")
			sb.WriteString("Example — \"build a web app with auth\":\n")
			sb.WriteString("```json\n")
			sb.WriteString("[\n")
			sb.WriteString("  {\"type\":\"compute\",\"tool\":\"compute\",\"params\":\"{\\\"goal\\\":\\\"build a Vue 3 + Express webapp with JWT auth and SQLite database\\\",\\\"mode\\\":\\\"deep\\\",\\\"query\\\":\\\"build a Vue 3 webapp with auth\\\"}\",\"tag\":\"build_webapp\"}\n")
			sb.WriteString("]\n")
			sb.WriteString("```\n")
			sb.WriteString("Note: ONE compute(deep) node — the architect inside decomposes into setup, coder tasks, execute/service, and validation phases. Do not split into multiple compute(deep) nodes (\"plan blueprint then plan code then plan tests\" is wrong — that all happens INSIDE the single compute call).\n\n")
		}
		sb.WriteString("## Rules\n")
		sb.WriteString("NEVER guess values you don't know. Only use names, paths, and parameters that are visible in the evidence (workspace files, blueprint, conversation). If you don't know the exact service name, file path, or port — plan a diagnostic step first (file_read, service list, bash ls) to discover it.\n")
		sb.WriteString("NEVER interpret, judge, or refuse requests.\n")
		sb.WriteString("ALWAYS write a step reference as `${step.<tag>.<path>}`, naming the step by its tag — never by its position, and never a literal value you do not have. It works as a whole parameter value and inside a longer string, such as a shell command.\n")
		sb.WriteString("NEVER write code in bash params.\n")
		sb.WriteString("NEVER use bash for complex multi-step tasks.\n")
		sb.WriteString("NEVER use interactive commands.\n")
		sb.WriteString("Use compute (type:\"compute\") for coding, development, and analytics work that the aggregator can't do — see the Compute Nodes decision rules above. Provide the GOAL, not the code.\n")
		sb.WriteString("ALWAYS use the compute mode preflight selected — see the Compute Nodes section above. Workspace contents do NOT influence this choice.\n")
		sb.WriteString("ALWAYS use the service tool for long-running processes (servers, daemons, dev servers, watchers, listeners). NEVER use bash for foreground servers — bash blocks the investigation waiting for the command to exit, which servers never do. service(action=\"start\", name=\"...\", command=\"...\", port=NNNN) spawns in the background and returns immediately. ALWAYS include the port parameter so health checks know which port to verify.\n")
		sb.WriteString("ALWAYS use bash only for commands that terminate: ls, grep, git, npm install, curl, node script.js, etc.\n")
		sb.WriteString("ALWAYS gather current data from the web when the question needs it, using whichever tool in the index above does that. Never answer a question about the present from memory.\n")
		sb.WriteString("ALWAYS gather required data before grafting compute. Every compute node must have its inputs supplied by prior gathering steps, wired with `${step.<tag>.<path>}` — never compute over inputs that don't exist yet.\n")
		sb.WriteString("ALWAYS plan and complete the full action the user asked for. A worklog showing prior failures is NOT a reason to plan a timid probe (e.g. just file_read) instead of the real edit/build — the user is asking again, plan the action. Imperatives ('do it', 'fix it', 'just do it') force action; never stop partway or ask for permission.\n")
		sb.WriteString("ALWAYS settle what you do not know BEFORE you plan around it. Read the objective and name the concrete values the plan needs — a path, a directory, a host, a user, a port, a process id, a version, a filename, an address — then ask of each one: have I been given this, or am I supplying it myself? A value you have not seen is a guess. It will look right and the step built on it fails on the value, not on the tool, so the failure teaches nothing and the retry guesses again.\n")
		sb.WriteString("ALWAYS treat this machine as unknown. You do not know its user, its home directory, its working directory, what is installed, what is running, what any path contains, or what any of its addresses answer — unless a step in THIS plan returned it, or it is stated above. A default that is usually right elsewhere is still a guess here. When the objective names something you cannot see (\"the home directory\", \"the config\", \"the service\", \"the latest one\"), that name is the question, not the answer.\n")
		sb.WriteString("ALWAYS plan the lookup as its own first step and wire its result forward with `${step.<tag>.<path>}`. The tool list holds tools that report the environment, the filesystem and the process table; choosing one costs a step and settles the value for the whole plan.\n")
		sb.WriteString("ALWAYS build functional products that work end-to-end. If building a webapp or UI, deliver a complete, clean, working experience — not a skeleton with TODO comments.\n")
		sb.WriteString("ALWAYS include a final verification step that proves the goal has been achieved. For services: curl/http check that it responds. For scripts: run on sample input and check output. For data pipelines: run test data through and verify result shape. Never end a plan without verification — 'wrote the files' is not achievement.\n")
		// The workspace layout describes where compute puts what it builds, so
		// it belongs with compute — see above.
		if slices.Contains(relevant, computeToolName) {
			sb.WriteString("\n## Workspace Layout\n")
			sb.WriteString("- project/ — source code, application files\n")
			sb.WriteString("- media/ — downloaded media (images, videos, audio). ALWAYS save downloads here: yt-dlp -o 'media/%(title)s.%(ext)s', curl -o media/file.jpg, etc.\n")
			sb.WriteString("- blueprints/ — architecture blueprints (auto-managed by compute)\n")
			sb.WriteString("- canvas/ — visual output that RENDERS LIVE in the UI (HTML pages, charts, images). Put any chart, plot, diagram, or interactive view here so the user actually SEES it.\n")
			sb.WriteString("- uploads/<session-id>/ — user-uploaded attachments. When the query has an [attached files] preamble, the paths land here. Each file may have <name>.meta.json (preview) and <name>.summary.md (LLM summary) sidecars.\n")
			// Written by tools rather than by a plan, and named here because a
			// step that chains on one of them should reference the field that
			// holds the path, not guess the path. Kept out of the list above:
			// that list is the five zones a plan may WRITE to, and it mirrors
			// internal/workspace.AllowedZones.
			sb.WriteString("\nThese are written FOR you and are read-only. Never guess a path in them — reference the field of the step that produced it:\n")
			sb.WriteString("- fetched/ — the whole page each web_fetch retrieved. Reach it through that step's `full_content_path`.\n")
			sb.WriteString("- output/ — the whole output each bash command printed. Reach it through that step's `output_path`.\n")
			sb.WriteString("- sessions/<session-id>/ — working files from earlier turns of a conversation. Not listed below; name the path directly when an earlier turn's material is needed.\n")
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

		// A ceiling stated on its own reads as an allowance to spend: every
		// recorded plan on a 30-step ceiling came back with 30 or 31 steps, and
		// the surplus steps were filled with whatever the context happened to
		// mention rather than with the objective, which is what the answer was
		// then written from. The limits still hold; the wording asks for a plan
		// sized to the work.
		sb.WriteString(budgetLine(a.cfg.MaxNodes, a.cfg.MaxLLMCalls))
	}

	rolePrompt := sb.String() + a.environmentSection()
	return rolePrompt
}

/*
 * PlanResult contains the planner output: steps and optionally an inferred intent.
 * desc: Wraps the parsed plan steps, declared capability gaps, inferred intent
 *       level, and whether intent was auto-inferred.
 */
type PlanResult struct {
	Steps          []PlanStep
	InferredIntent gates.Intent // only set when intent was auto-inferred
	WasAuto        bool         // true if the planner inferred intent

	// Tools is what the planner was shown, in the order it was shown them, and
	// Objective is the text they were ranked against.
	//
	// Carried out of the planner so the trace can show them. A plan that
	// reaches for the wrong tool and a plan that was never shown the right one
	// look identical from the outside, and the difference is the first thing
	// anyone reading a bad run needs.
	Tools     []string
	Objective string
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

// ── The plan schema ────────────────────────────────────────────────────────

// executivePlanSchemaTemplate is the plan schema with a %s placeholder
// where the intent enum goes. The enum is built at call time from the
// registry so custom intent names (admin-created via the UI) show up as
// valid values to the model.
var executivePlanSchemaTemplate = `{
	"type": "object",
	"properties": {
		"answer": {
			"type": "string",
			"description": "Write \"\" unless steps is empty. It is only filled when the message genuinely requires no operation — see Planning completeness."
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
				"required": ["tool", "params", "tag"],
				"properties": {
					"type":       {"type": "string", "enum": ["tool","compute"], "description": "Node type: tool (default) or compute (LLM code generation)"},
					"tool":       {"type": "string", "description": "Tool name from the Tools list"},
					"params":     {"type": "string", "description": "The tool's input parameters, as a JSON object written INSIDE A STRING. Example: \"{\\\"query\\\": \\\"search terms\\\"}\". ALWAYS populate for tools with required params marked *; write \"{}\" when the tool takes none, never an empty string. A value is either something you have — \"{\\\"command\\\": \\\"ls -la\\\"}\" — or a REFERENCE to an earlier step's output, written ${step.<that step's tag>.<dot-path into its output>}. Example: a fetch reading the first result of a search tagged find_docs is \"{\\\"url\\\": \\\"${step.find_docs.results.0.url}\\\"}\"."},
					"depends_on": {"type": "array", "items": {"type": "integer"}, "description": "Rarely needed. A step that references another already depends on it, and the wiring is done for you. Use this ONLY to order two steps that pass no data between them."},
					"tag":        {"type": "string", "description": "This step's name, unique within the plan: letters, digits, _ or - with no spaces. Other steps reference this step by it."}
				}
			}
		}
	},
	"required": ["steps"]
}`

/*
 * executivePlanSchema returns the shape a plan takes.
 * desc: One schema, named "plan", matching the PlanStep array. The model is
 *       pinned to it and answers with the whole plan as its argument. It is not
 *       a tool — nothing registers it and nothing executes it; llm.ToolDef is
 *       simply the shape a provider takes a schema in.
 *
 *       The intent enum is built at call time from the registry, so an intent
 *       an operator added is a value the model may return.
 * return: the schema, in the shape the provider takes.
 */
func (a *Agent) executivePlanSchema() llm.ToolDef {
	// Build the intent enum dynamically from the registry. If the registry
	// hasn't been loaded the enum is omitted entirely — Go has no knowledge
	// of specific intent names to fall back on.
	var names []string
	if a.intentRegistry != nil {
		names = a.intentRegistry.AllowedNames(-1)
	}
	enumJSON, _ := json.Marshal(names)
	schema := json.RawMessage(fmt.Sprintf(executivePlanSchemaTemplate, string(enumJSON)))
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "plan",
			Description: `Submit an execution plan. params is a STRING holding a JSON object. Example: {"steps":[{"tool":"web_search","params":"{\"query\":\"bitcoin price\"}","tag":"find_price"}]}. Chaining — the second step REFERENCES the first by its tag, and that is the whole wiring: {"steps":[{"tool":"web_search","params":"{\"query\":\"news\"}","tag":"find_news"},{"tool":"web_fetch","params":"{\"url\":\"${step.find_news.results.0.url}\"}","tag":"read_news"}]}. Trivial: {"steps":[],"answer":"Paris."}`,
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

/*
 * linkDependsOnTags resolves depends_on entries that name a step's tag.
 * desc: FlexInts.UnmarshalJSON can only hold positions, so a name is dropped as
 *       it decodes and the step silently loses a dependency — the scheduler then has
 *       nothing to make it wait, and validatePlanWiring, whose whole check is
 *       "declared a dependency but wired no value", returns early because the
 *       declaration is the thing that went missing. A re-plan is where this
 *       bites: positions restart with each plan and tags do not, so a reflector
 *       naming an earlier step writes the one form that gets thrown away.
 *
 *       Reads the same JSON the steps were decoded from, matches each name
 *       against the plan's own tags, and records the rest on the step.
 * param: stepsArray - the raw JSON array the steps were decoded from
 * param: steps - the decoded steps, updated in place
 */
func linkDependsOnTags(stepsArray json.RawMessage, steps []PlanStep) {
	var probe []struct {
		DependsOn []json.RawMessage `json:"depends_on"`
	}
	if len(stepsArray) == 0 || json.Unmarshal(stepsArray, &probe) != nil {
		return
	}
	byTag := make(map[string]int, len(steps))
	for i := range steps {
		if steps[i].Tag != "" {
			byTag[steps[i].Tag] = i
		}
	}
	for i := range probe {
		if i >= len(steps) {
			break
		}
		for _, entry := range probe[i].DependsOn {
			var name string
			if json.Unmarshal(entry, &name) != nil {
				continue // a number, already carried as a position
			}
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, isNumber := strconv.Atoi(name); isNumber == nil {
				continue // a position written as a string, already carried
			}
			idx, known := byTag[name]
			if !known || idx == i {
				steps[i].UnresolvedDeps = append(steps[i].UnresolvedDeps, name)
				continue
			}
			if !slices.Contains(steps[i].DependsOn, idx) {
				steps[i].DependsOn = append(steps[i].DependsOn, idx)
				log.Printf("[dag] plan: step %d depends on %q → step %d", i, name, idx)
			}
		}
	}
}

/*
 * validatePlanNames reports names a reference could not resolve to one step.
 * desc: The fourth plan-time validator, and the one the other three assume. A
 *       name is what a reference resolves against, so it has to be unique
 *       within the plan and has to be something a reference can spell.
 *
 *       Neither was checked while a name was only a label on a trace line. Two
 *       steps could share one, and stepIndexFor would take the first — so a
 *       reference meant for the second read the first, silently. And a name
 *       could hold a space or a bracket, which no reference can address at all.
 * param: steps - the plan
 * return: one message per unusable name, empty when the plan is clean
 */
func validatePlanNames(steps []PlanStep) []string {
	var errs []string
	seen := make(map[string]int, len(steps))
	for i := range steps {
		name := steps[i].Tag
		if name == "" {
			continue // unnamed steps are addressed by position; nothing to clash
		}
		if !stepNameRe.MatchString(name) {
			errs = append(errs, fmt.Sprintf(
				"step %d (%s): the name %q cannot be referenced — a name is letters, "+
					"digits, _ or - with no spaces, dots or brackets, because a reference "+
					"is written ${step.<name>.<field>}",
				i, steps[i].Tool, name))
			continue
		}
		if first, dup := seen[name]; dup {
			errs = append(errs, fmt.Sprintf(
				"step %d (%s): the name %q is already step %d's — a reference naming it "+
					"cannot say which step it means; give every step its own name",
				i, steps[i].Tool, name, first))
			continue
		}
		seen[name] = i
	}
	return errs
}

/*
 * validatePlanComputeInputs reports a compute that reads nothing while the plan
 * gathers something.
 * desc: A compute receives what is wired into it and nothing else. It cannot
 *       reach the graph, cannot see the step beside it, and does not run again
 *       when one finishes: its data is fixed at the moment it starts. So a
 *       compute with no reference has no data for the whole of its life, and a
 *       stage asked for a calculation it has no figures for will supply the
 *       figures.
 *
 *       Observed: a plan searched, fetched, and ran a compute that referenced
 *       neither. All three started in the same second; the compute then ran for
 *       eighty seconds while both results sat in the graph where it could not
 *       reach them, and wrote the numbers it needed as constants under a comment
 *       naming the source they should have come from. Every check the engine
 *       runs passed, because the compute schema requires a goal and a mode and
 *       a reference is optional.
 *
 *       Only reported when the plan HAS something to wire. A calculation that
 *       genuinely needs nothing — the thousandth prime, a conversion — is a
 *       plan with no other step to read from, and is left alone. A step that
 *       reads THIS compute is downstream and does not count: it is the output,
 *       not an input.
 *
 *       It reaches the planner as a correction rather than dropping the step,
 *       which is what validatePlanWiring does and why it could not be used here.
 *       A run whose compute is deleted has no calculation at all; a run whose
 *       planner is told to wire it has one that works.
 * param: steps - the plan
 * return: one message per compute that ignores what the plan gathered
 */
func validatePlanComputeInputs(steps []PlanStep) []string {
	var errs []string
	for i, s := range steps {
		if s.Type != "compute" && s.Tool != "compute" {
			continue
		}
		// A step that declares a dependency and wires nothing is the same fault
		// wearing a different shape, and validatePlanWiring already owns it — it
		// drops the step further down. Reporting it here as well would put two
		// remedies on one fault: this one fails the run after three corrections,
		// that one lets the run continue without the step. The split is by
		// declaration, so each fault has exactly one owner.
		if len(s.DependsOn) > 0 {
			continue
		}
		// task_files is the other way data reaches a compute: it reads them off
		// disk itself rather than being handed their content.
		if hasNonEmptyTaskFiles(s.Params) {
			continue
		}
		if len(stepRefsIn(s.Params, steps)) > 0 {
			continue
		}
		var available []string
		for j, other := range steps {
			if j == i || readsStep(other, i, steps) {
				continue
			}
			available = append(available, stepLabelFor(other, j))
		}
		if len(available) == 0 {
			continue // nothing in this plan could have fed it
		}
		errs = append(errs, fmt.Sprintf(
			"step %d (compute, tag %q): reads nothing from any step, while this plan runs %s. "+
				"A compute is given only what its params reference — it cannot reach the graph, and its "+
				"data is fixed when it starts, so a step finishing later does not reach it. Wire what it "+
				"needs with ${step.<tag>.<field>}, or, if the calculation genuinely needs nothing this "+
				"plan gathers, drop the steps that gather it",
			i, s.Tag, strings.Join(available, ", ")))
	}
	return errs
}

// readsStep reports whether a step references the step at position idx, which
// makes it downstream of it — its output, not a possible input.
func readsStep(s PlanStep, idx int, steps []PlanStep) bool {
	for _, r := range stepRefsIn(s.Params, steps) {
		if r.idx == idx {
			return true
		}
	}
	return false
}

// stepLabelFor names a step the way the planner wrote it, for a message it has
// to act on.
func stepLabelFor(s PlanStep, i int) string {
	if s.Tag != "" {
		return fmt.Sprintf("%s (%s)", s.Tag, s.Tool)
	}
	return fmt.Sprintf("step %d (%s)", i, s.Tool)
}

/*
 * validatePlanDeps reports dependencies that name nothing in the plan.
 * desc: The plan-time twin of the other two validators: validatePlanReferences checks
 *       the values wired between steps, validatePlanParams the parameters of a
 *       step, and this the dependencies of one. A name that matches no tag and
 *       no position is returned so the executive re-plans against it.
 * param: steps - the plan
 * return: one message per unresolved dependency, empty when the plan is clean
 */
func validatePlanDeps(steps []PlanStep) []string {
	var errs []string
	for i := range steps {
		for _, name := range steps[i].UnresolvedDeps {
			tags := make([]string, 0, len(steps))
			for j := range steps {
				if steps[j].Tag != "" && j != i {
					tags = append(tags, strconv.Quote(steps[j].Tag))
				}
			}
			available := "this plan has no other tagged steps"
			if len(tags) > 0 {
				available = "the steps in this plan are tagged " + strings.Join(tags, ", ")
			}
			errs = append(errs, fmt.Sprintf("step %d (%s): depends_on names %q, which is neither a step position nor any step's tag — %s",
				i, steps[i].Tool, name, available))
		}
	}
	return errs
}

// parseExecutivePayload handles LLMs returning steps as a JSON string instead of an array.
func parseExecutivePayload(raw string, payload *executiveCallPayload) error {
	// The steps array as it arrived, so a depends_on entry naming a tag can be
	// matched before FlexInts decodes it away.
	var arrived struct {
		Steps json.RawMessage `json:"steps"`
	}
	_ = json.Unmarshal([]byte(raw), &arrived)

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
			linkDependsOnTags(json.RawMessage(stepsStr), payload.Steps)
			return nil
		}
		return err
	}
	linkDependsOnTags(arrived.Steps, payload.Steps)
	return nil
}

/*
 * objective is what this run is trying to do, in the words it was given.
 * desc: The user's request verbatim, then preflight's framing of it — which is
 *       where the URLs, paths and identifiers from the conversation are — then
 *       the re-plan frame when the reflector asked for one, which says what the
 *       plan so far did not achieve.
 *
 *       One function because two callers must not disagree: the planner reads
 *       this text, and the tool search ranks against it. If they were built
 *       separately the search would drift from the prompt, and the drift would
 *       show up as a planner reaching for tools it was never shown.
 * param: trigger - what started the run.
 * param: graph - the run, for preflight's framing. May be nil.
 * param: replanFrame - the reflector's next move, when re-planning.
 * return: the objective text.
 */
func (a *Agent) objective(trigger Trigger, graph *Graph, replanFrame ...string) string {
	out := a.formatTrigger(trigger)
	if graph != nil && graph.Preflight != nil && !graph.Preflight.Context.Empty() {
		out += "\n\n## Context\n" + graph.Preflight.Context.Text()
	}
	// Appended above the worklog block the caller adds, so the frame's
	// reference to "the worklog below" stays accurate and the user's goal stays
	// verbatim at the top.
	if len(replanFrame) > 0 && replanFrame[0] != "" {
		out += replanFrame[0]
	}
	return out
}

/*
 * runExecutiveNative makes a single LLM call for the plan.
 * desc: Sends the plan schema and pins the model to it, so the whole plan comes
 *       back as one argument. No text parsing, no markdown fences. Falls back to
 *       text parsing if the model answers with prose instead.
 * param: ctx - context for the LLM call.
 * param: trigger - the investigation trigger.
 * return: PlanResult pointer with steps and intent, or error.
 */
/*
 * planRetryAdvice tells the planner what it got wrong, in the terms of the
 * mistake it actually made.
 * desc: The retry always sent one fixed sentence — "goal, mode, query go INSIDE
 *       params" — whatever had broken. A plan that put a ${step.N.field}
 *       reference where the params STRING belongs was answered with advice
 *       about a different error, and failed the same way twice: two model
 *       calls, several minutes, and a run that ended with nothing investigated.
 *
 *       A second attempt is only worth making if it is told something the first
 *       did not know. Each branch shows the shape that would have parsed,
 *       because a corrected example is what the model can copy; the error alone
 *       says only that it is wrong.
 * param: err - what the parse rejected.
 * param: raw - the arguments as they arrived, to tell the mistakes apart.
 * return: the tool reply the retry is given.
 */
func planRetryAdvice(err error, raw string) string {
	const opening = "Error: %v.\n\n%s\n\nFix that and call plan() again."

	text := err.Error()
	switch {
	// params did not parse, and a reference is why. The reference syntax and
	// the params-as-a-string rule meet here: the reference belongs INSIDE the
	// string, and this put it where the string should be.
	case strings.Contains(text, "params must be a JSON object") && strings.Contains(raw, "${"):
		return fmt.Sprintf(opening, err,
			"params is a STRING containing a JSON object, and a ${step.N.field} reference goes INSIDE "+
				"that string as a value — it never replaces the object itself.\n"+
				"  wrong:   \"params\": ${step.2.pid}\n"+
				"  wrong:   \"params\": {\"pid\": ${step.2.pid}}\n"+
				"  correct: \"params\": \"{\\\"pid\\\": \\\"${step.2.pid}\\\"}\"")

	// params did not parse for some other reason.
	case strings.Contains(text, "params must be a JSON object"):
		return fmt.Sprintf(opening, err,
			"params is a STRING whose contents are a JSON object, not an object and not bare text.\n"+
				"  correct: \"params\": \"{\\\"path\\\": \\\"/tmp/x\\\"}\"")

	// A field a step does not have, which is the shape the original advice was
	// written for. Kept, because that failure is real and still happens.
	case strings.Contains(text, "unknown field") || strings.Contains(text, "cannot unmarshal"):
		return fmt.Sprintf(opening, err,
			"goal, mode and query belong INSIDE params, not beside tool and tag at the step level.")
	}
	return fmt.Sprintf(opening, err, "Return the same plan with the JSON corrected.")
}

func (a *Agent) runExecutiveNative(ctx context.Context, trigger Trigger, graph *Graph, replanFrame ...string) (planOut *PlanResult, errOut error) {
	// The objective is built before the tools are chosen, and it is the same
	// text the planner is about to read. That order is the point.
	//
	// Tool choice used to come from preflight, which runs once at the top of a
	// turn. A re-plan therefore got the tools the FIRST plan was given, while
	// the one piece of text that said what was now missing — the reflector's
	// next move, inside the frame — went into the prompt and nowhere near the
	// search. A run that needed a tool it had not been shown could not ask for
	// one, could not be given one, and concluded the work impossible.
	objective := a.objective(trigger, graph, replanFrame...)
	relevant := a.relevantTools(ctx, graph, trigger, objective)
	// Recorded on whatever plan this call produces — see PlanResult.Tools.
	defer func() {
		if planOut != nil {
			planOut.Tools, planOut.Objective = relevant, objective
		}
	}()
	log.Printf("[dag] executive (native) sees %d tools: %v", len(relevant), relevant)
	if len(a.skillGuidance) > 0 {
		log.Printf("[dag] executive (native) has %d skill cards loaded", len(a.skillGuidance))
	}

	dagMode := a.cfg.DAGMode
	if trigger.DAGMode != "" {
		dagMode = trigger.DAGMode
	}
	// The intent's NAME, not its rank.
	//
	// gates.Intent renders as "rank(100)", which is what the prompt used to
	// carry — while the schema asks the planner to return one of the registry's
	// names. Nothing told it that 100 is "operate", so the field it filled in
	// was a guess: an operate-level request came back declaring "observe", and
	// an intent set below what the work needs blocks every tool the plan
	// reaches for.
	intent := intentName(a.intentRegistry, trigger.Intent())
	if trigger.Intent() == gates.IntentAuto && graph != nil && graph.Preflight != nil {
		intent = intentName(a.intentRegistry, graph.Preflight.Intent)
		log.Printf("[dag] executive (native) intent from preflight: %s", intent)
	}

	executiveHistory := prefixAssistantHistory(trigger.History)

	// One gate call for all runtime context the planner needs: worklog
	// (system state) plus the tool index (signatures + output schemas so
	// ${step.N.field} placeholders can be wired against correct result shapes).
	userQuery := objective
	var toolIndex string
	if graph != nil && graph.Context != nil {
		gateResp, gerr := graph.Context.Get(ctx, ContextRequest{
			ReturnSources: Sources(
				Worklog(20, "all"),
				ToolIndex(relevant),
			),
			// Enough for the tool index relevantTools just sized, plus the
			// worklog beside it. At 8000 the gate trimmed the index on every
			// call — the schemas a planner needs to write ${step.N.field}
			// against were the thing being cut.
			MaxBudget: a.budget(toolIndexBudget) + 8000,
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

	// Where the run stands, and what the evidence leaves open, for the stage
	// that decides what runs next.
	//
	// Only on a re-plan. A first plan has nothing to be told: no step has run,
	// so EdgeReFrame finds no material and returns nothing anyway — the guard
	// is here so the intent is readable rather than a consequence of an empty
	// graph.
	//
	// Its own reader rather than the reflector's, though the two see identical
	// material: the reflector is asked whether more is worth doing, and this
	// stage is asked what would produce what is missing. Same run, different
	// question, and the questions are the point. That costs one light-model
	// call per re-plan round.
	if len(replanFrame) > 0 {
		sysPromptN, userQuery = WithReframe(sysPromptN, userQuery,
			a.EdgeReFrame(ctx, graph, objective,
				ReframeToPlanner))
	}

	// What the run has already produced, as the calls that produced it.
	//
	// The planner is the stage that writes params, and it was the one stage
	// never shown a value: its context was the worklog and the tool index, and
	// a node's fields are in neither. The only route from a step to the plan
	// that followed it was a sentence the reflector retyped — so a path that
	// was correct and on disk arrived as a placeholder that resolved to
	// nothing, and the plan used one the model half-remembered.
	messages := BuildMessagesWithResults(sysPromptN, userQuery, executiveHistory, graph.Arcs())

	ctx = withTrace(ctx, TraceID{
		NodeID:   "executive",
		NodeType: "executive_native",
		Tag:      "plan",
		Input: map[string]string{
			"dag_mode": dagMode,
			"intent":   intent,
		},
	})
	resp, err := a.completeHeavy(ctx, &llm.ChatRequest{
		Messages: messages,
		Tools:    []llm.ToolDef{a.executivePlanSchema()},
		// PIN the model to `plan` — not just "call some tool". A weak reasoning
		// model, seeing web_search/web_fetch named all over the guidance, otherwise
		// emits a direct tool call instead of wrapping it in a plan; that hard-fails
		// with "planner called unexpected tool" and there's no retry for it.
		ToolChoice:  llm.ForceToolChoice("plan"),
		Temperature: a.cfg.Temperature,
		MaxTokens:   a.planMaxTokens(ctx),
	})

	// The planner is not a node, so it records itself — see debugrecord.go. The
	// plan it produced is filled in at the bottom, once it has parsed; a call
	// that failed records here and stops.
	planRec := DebugRecord{
		ID: "executive", Kind: "executive", Label: "plan", Round: graph.Round(),
		System: sysPromptN, User: userQuery,
	}
	if graph.Round() > 0 {
		planRec.ID = fmt.Sprintf("executive-r%d", graph.Round())
		planRec.Label = fmt.Sprintf("replan %d", graph.Round())
	}
	// One record however this returns. The plan has several exits — two that
	// succeed, several that fail, and a retry between them — and a record
	// written at each would be written differently at each.
	defer func() {
		if errOut != nil && planRec.Err == "" {
			planRec.Err = errOut.Error()
		}
		if planOut != nil {
			planRec.Out, _ = json.Marshal(planOut)
		}
		graph.recordStage(planRec)
	}()

	if err != nil {
		return nil, fmt.Errorf("planner LLM call (native): %w", err)
	}

	if len(resp.Choices) == 0 {
		traceFault(ctx, "no choices")
		planRec.Err = "no choices"
		return nil, fmt.Errorf("planner LLM returned no choices")
	}
	planRec.TokensIn, planRec.TokensOut = resp.Usage.PromptTokens, resp.Usage.CompletionTokens

	choice := resp.Choices[0]
	if len(choice.Message.ToolCalls) > 0 {
		planRec.Reply = choice.Message.ToolCalls[0].Function.Arguments
	} else {
		planRec.Reply = choice.Message.Content
	}

	// This call and its retry below use completeHeavy rather than the checked
	// variant on purpose: this stage does something better than reporting the
	// truncation, which is to ask for a shorter plan. The checked variant would
	// return an error here and the retry would never run.
	//
	// The reply ran into its cap and stopped mid-JSON. Providers report this
	// either as finish_reason "length" — where the tool-call branch below is
	// skipped and the fragment would be returned to the user as if the planner
	// had chosen to answer in prose — or as "tool_calls" with truncated
	// arguments, where the parse retry fires and blames the wrong thing. Neither
	// tells the model what happened, and both retry under the same cap.
	if choice.FinishReason == "length" {
		log.Printf("[dag] executive plan cut off at %d tokens — asking for a shorter plan", a.planMaxTokens(ctx))
		shorter := append(append([]llm.Message{}, messages...), llm.Message{
			Role: "user",
			Content: "Your previous plan was cut off before it finished — it ran past the reply limit. " +
				"Call plan() again with fewer, larger steps, and shorter parameter values.",
		})
		retryResp, retryErr := a.completeHeavy(retracing(ctx, "plan_shorter"), &llm.ChatRequest{
			Messages:    shorter,
			Tools:       []llm.ToolDef{a.executivePlanSchema()},
			ToolChoice:  llm.ForceToolChoice("plan"),
			Temperature: a.cfg.Temperature,
			MaxTokens:   a.planMaxTokens(ctx),
		})
		if retryErr == nil && len(retryResp.Choices) > 0 {
			choice = retryResp.Choices[0]
		}
	}

	// Did the plan come back in the shape it was asked for
	if choice.FinishReason == "tool_calls" && len(choice.Message.ToolCalls) > 0 {
		tc := choice.Message.ToolCalls[0]

		// It named a tool from the index instead of wrapping it in a plan.
		//
		// Only one tool is offered on this call and the choice is pinned to it,
		// so the name it produced is not a tool it could reach — it is a word
		// it read in the tool index and emitted as a call. A small model does
		// this occasionally, and it did it more once the index grew from three
		// tools to twenty-seven.
		//
		// It used to end the run. A single slip on a re-plan threw away every
		// step that had already succeeded, which is what it cost in the Solana
		// run: seven steps done, the re-plan mistyped its call, and the whole
		// thing came back as a failure. Ask again, naming the mistake.
		if tc.Function.Name != "plan" {
			log.Printf("[dag] executive called %q instead of plan — asking again", tc.Function.Name)
			again := append(append([]llm.Message{}, messages...), llm.Message{
				Role: "user",
				Content: fmt.Sprintf("You called %q. That is not something you can call here — the only "+
					"call available is plan(), and %q is a tool for a STEP INSIDE a plan. "+
					"Call plan() and put that tool in a step.", tc.Function.Name, tc.Function.Name),
			})
			retryResp, retryErr := a.completeHeavy(retracing(ctx, "plan_wrap"), &llm.ChatRequest{
				Messages:    again,
				Tools:       []llm.ToolDef{a.executivePlanSchema()},
				ToolChoice:  llm.ForceToolChoice("plan"),
				Temperature: a.cfg.Temperature,
				MaxTokens:   a.planMaxTokens(ctx),
			})
			if retryErr != nil || len(retryResp.Choices) == 0 ||
				len(retryResp.Choices[0].Message.ToolCalls) == 0 ||
				retryResp.Choices[0].Message.ToolCalls[0].Function.Name != "plan" {
				return nil, fmt.Errorf("planner called unexpected tool %q (expected plan)", tc.Function.Name)
			}
			choice = retryResp.Choices[0]
			tc = choice.Message.ToolCalls[0]
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
				llm.Message{Role: "tool", ToolCallID: tc.ID, Name: "plan", Content: planRetryAdvice(err, fixedRaw)},
			)
			retryResp, retryErr := a.completeHeavyChecked(retracing(ctx, "plan_reparse"), &llm.ChatRequest{
				Messages:    retryMessages,
				Tools:       []llm.ToolDef{a.executivePlanSchema()},
				ToolChoice:  llm.ForceToolChoice("plan"),
				Temperature: 0.1,
				MaxTokens:   a.planMaxTokens(ctx),
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

		// Empty steps = trivial query, planner answered directly.
		//
		// True on a first plan, and not on a re-plan. Stopping is the
		// reflector's decision: its three outcomes are continue, replan and
		// conclude, and conclude is how a run stops. A re-plan happens only
		// because the reflector chose replan, which is it ruling that work
		// remains — so an empty plan here is the planner overturning a
		// decision that is not its own.
		//
		// Read from a run that did exactly that. The reflector asked for a
		// specific fetch; the planner returned no steps and an answer recalled
		// from memory; the run ended having done nothing it was asked to do.
		// Its answer then had to be walked back by the stage that writes the
		// reply, which said plainly that no calculation had been performed.
		if len(steps) == 0 && len(replanFrame) > 0 {
			log.Printf("[dag] warning: the planner has no further step to add; the reflector had asked for more work, so the run stops here")
			return nil, &ExecutiveNoMove{Answer: payload.Answer}
		}
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
			// The list of real tools is right there in the message; naming two of
			// them by hand as well told an application that registers neither to
			// call tools that do not exist — inside the message correcting the
			// model for calling tools that do not exist.
			correction := fmt.Sprintf(
				"Error: %s not callable tools — they may be skills or capabilities, not tools. "+
					"Call plan() again using ONLY real tools from this list: %s.",
				quoteList(unknown), strings.Join(relevant, ", "))
			replanMessages := append(messages,
				llm.Message{Role: "assistant", Content: "", ToolCalls: choice.Message.ToolCalls},
				llm.Message{Role: "tool", ToolCallID: tc.ID, Name: "plan", Content: correction},
			)
			replanResp, replanErr := a.completeHeavyChecked(retracing(ctx, "plan_real_tools"), &llm.ChatRequest{
				Messages:    replanMessages,
				Tools:       []llm.ToolDef{a.executivePlanSchema()},
				ToolChoice:  llm.ForceToolChoice("plan"),
				Temperature: 0.1,
				MaxTokens:   a.planMaxTokens(ctx),
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

		// Validate the plan and correct it with feedback before running it. Two
		// checks, both at plan time, both feeding this one re-plan loop:
		//
		//   - validatePlanReferences — the wiring BETWEEN steps: a ${step.N.field} that
		//     points at its own step, a step index not in the plan, or a field the
		//     producing tool never emits (classically ${step.0.results.0.url} off a
		//     web_fetch, when a results[] list only comes from web_search).
		//   - validatePlanParams — the parameters OF a step: a parameter name the
		//     called tool doesn't declare (e.g. web_search(topn: …), where the count
		//     knob is max_results). Caught here the planner re-plans with the real
		//     names; left to dispatch, the call is rejected and the node just fails.
		//
		// Loop: validate → if broken, re-plan with the exact errors as feedback →
		// re-validate the NEW plan → repeat, up to maxPlanCorrections times. If the
		// plan still isn't clean after that many tries, FAIL the run rather than
		// dispatch a plan we already know is broken.
		//
		// curToolCalls / curToolCallID track the plan() call that produced the steps
		// being validated, so each correction is threaded onto the attempt it's
		// correcting (initially the original plan; after a re-plan, that re-plan).
		const maxPlanCorrections = 3
		curToolCalls := choice.Message.ToolCalls
		curToolCallID := tc.ID
		for corrections := 0; ; corrections++ {
			nameErrs := validatePlanNames(steps)
			refErrs := validatePlanReferencesIn(steps, a.registry, graph)
			paramErrs := validatePlanParams(steps, a.registry)
			depErrs := validatePlanDeps(steps)
			inputErrs := validatePlanComputeInputs(steps)

			// Two kinds of fault, and only one of them can be certain.
			//
			// A name that matches nothing, a parameter a tool does not take, a
			// reference to a step that is not there: each is wrong however the
			// run turns out, so a plan carrying one is not dispatched.
			//
			// A compute that reads nothing is a plan that will probably invent
			// its data — not a plan that cannot run. Judging it needs to know
			// whether the goal requires what the plan gathered, and nothing here
			// knows that. So the planner is told, up to the same three times, and
			// if it still says no the plan runs as written: refusing would end
			// runs over a suspicion, and each correction is a call to the heavy
			// model with the whole plan prompt behind it.
			//
			// What survives is not unguarded. A compute with nothing wired is
			// told so in its own prompt — see buildComputeUserPrompt — which is
			// the layer that knows what it actually received.
			fatal := append(append(append(append([]string{}, nameErrs...), refErrs...), paramErrs...), depErrs...)
			allErrs := append(append([]string{}, fatal...), inputErrs...)
			if len(allErrs) == 0 {
				break // plan is clean — proceed
			}
			if corrections >= maxPlanCorrections {
				if len(fatal) > 0 {
					return nil, fmt.Errorf("executive: plan still invalid after %d corrections: %s",
						maxPlanCorrections, strings.Join(fatal, "; "))
				}
				log.Printf("[dag] executive kept a plan whose compute reads nothing after %d corrections: %s",
					maxPlanCorrections, strings.Join(inputErrs, "; "))
				break
			}
			log.Printf("[dag] executive plan has %d problem(s) [%d reference, %d param], correction %d/%d: %v",
				len(allErrs), len(refErrs), len(paramErrs), corrections+1, maxPlanCorrections, allErrs)
			correction := "Error: your plan is invalid. Fix EVERY problem below and call plan() again:\n- " +
				strings.Join(allErrs, "\n- ")
			if len(refErrs) > 0 {
				// Guidance for the error at hand. It used to be one paragraph
				// about web_search → web_fetch whatever the error was, so a
				// planner told its compute step referenced itself was answered
				// with advice about fetching pages, and spent all three
				// corrections none the wiser.
				correction += "\n\nHow ${step.N.field} works: it reads step N's output, so N must be an EARLIER step whose tool actually produces `field`."
				joined := strings.Join(refErrs, " ")
				if strings.Contains(joined, "OWN output") {
					correction += " A step numbered N cannot read ${step.N…}. If the value comes from work you have not planned, ADD the step that produces it BEFORE this one and reference that index; step numbers are positions in THIS plan, counted from 0."
				}
				if strings.Contains(joined, "does not produce") {
					correction += " Reference a field the producing tool actually returns. `content` is the rendered text of any tool's result; the tool's own fields are listed under `returns:` in the tool index above."
				}
				if strings.Contains(joined, "which does not exist") {
					correction += " Every index must be a step in THIS plan, counted from 0."
				}
				// A name that belongs to an EARLIER arc is the one case where the
				// generic advice is actively wrong: it says "use its exact tag",
				// and an exact tag is what the planner just used. Four identical
				// plans came back on one run because every correction restated the
				// move that had failed. The value is already in front of it as a
				// tool result, so the fix is to copy the value, not to re-address it.
				if strings.Contains(joined, "names no step in THIS plan") {
					correction += " A step that already RAN is not addressable from a new plan — positions and tags both reach only the steps you are writing now. What it returned is above, as a tool result: take the value out of it and write that value into the param, with depends_on:[]."
				}
			}
			if len(paramErrs) > 0 {
				correction += "\n\nUse ONLY the parameters each tool declares — never invent a parameter name."
			}
			// Fresh copy of the base messages each pass so the append can't mutate the
			// shared slice across iterations.
			replanMessages := append(append([]llm.Message{}, messages...),
				llm.Message{Role: "assistant", Content: "", ToolCalls: curToolCalls},
				llm.Message{Role: "tool", ToolCallID: curToolCallID, Name: "plan", Content: correction},
			)
			replanResp, replanErr := a.completeHeavyChecked(retracing(ctx, "plan_real_tools"), &llm.ChatRequest{
				Messages:    replanMessages,
				Tools:       []llm.ToolDef{a.executivePlanSchema()},
				ToolChoice:  llm.ForceToolChoice("plan"),
				Temperature: 0.1,
				MaxTokens:   a.planMaxTokens(ctx),
			})
			// A failed re-plan call (LLM error, or no usable steps) leaves `steps`
			// unchanged; the loop re-validates the same plan next pass and burns a
			// correction, so a persistently failing re-plan still hard-fails at the cap.
			if replanErr != nil || len(replanResp.Choices) == 0 || len(replanResp.Choices[0].Message.ToolCalls) == 0 {
				log.Printf("[dag] executive plan correction %d: re-plan call failed (%v) — re-checking", corrections+1, replanErr)
				continue
			}
			rtc := replanResp.Choices[0].Message.ToolCalls[0]
			var replanned executiveCallPayload
			if perr := parseExecutivePayload(fixComputeStepParams(rtc.Function.Arguments), &replanned); perr != nil || len(replanned.Steps) == 0 {
				log.Printf("[dag] executive plan correction %d: no usable steps (perr=%v) — re-checking", corrections+1, perr)
				continue
			}
			log.Printf("[dag] executive re-planned after validation (correction %d, %d steps)", corrections+1, len(replanned.Steps))
			steps = replanned.Steps
			payload = replanned
			curToolCalls = replanResp.Choices[0].Message.ToolCalls
			curToolCallID = rtc.ID
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
// ExecutiveNoMove says the planner had nothing to add to a re-plan.
//
// Not a failure, and not named as one. The reflector asked for more work and
// the planner has no step that would produce it — the two disagree, and the run
// is done. Disagreement between two stages is an ordinary thing for a run to
// arrive at, not a fault in either of them.
//
// It implements error only because that is the channel a plan attempt returns
// on. The scheduler reads the type, resolves the stage, and ends the run; it
// never treats this as a broken step.
//
// It stays a distinct outcome rather than an ordinary empty plan, because an
// empty plan on a FIRST plan means something else entirely: a question that
// needed no tools, whose answer is in Answer.
type ExecutiveNoMove struct {
	// Answer is whatever the planner wrote instead of steps, which is usually a
	// description of what is still missing. Empty when it wrote none.
	Answer string
}

// Error is the error interface, not a description of a fault: the sentence says
// what happened, so a log line carrying it reads as an outcome.
func (e *ExecutiveNoMove) Error() string {
	return "the planner had no further step to add; the reflector had asked for more work, so the run stops here"
}

/*
 * intentName renders an intent the way the planner is asked to write it.
 * desc: The registry owns the names; gates.Intent knows only ranks, and says so
 *       — see its String method. So the translation happens here, where both are
 *       in reach, rather than being left to a model reading "rank(100)" and
 *       choosing from a list of words.
 * param: reg - the intent registry, or nil.
 * param: i - the rank, or IntentAuto.
 * return: the registry's name for that rank, falling back to the rank itself.
 */
func intentName(reg *IntentRegistry, i gates.Intent) string {
	if i == gates.IntentAuto {
		return "auto"
	}
	if reg != nil {
		if name := reg.NameByRank(int(i)); name != "" {
			return name
		}
	}
	return i.String()
}

// unknownToolNames returns the distinct step tools that don't exist in the
// registry. It's the pre-execution existence check: a non-empty result means
// the planner named something callable that isn't — the signal to re-plan
// rather than drop-and-fall-back.
func (a *Agent) unknownToolNames(steps []PlanStep) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range steps {
		if s.Tool == "" || seen[s.Tool] {
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
	// Fill in each step's target from the run's, guided by what the tool says
	// it needs. Done here rather than trusting the planner to repeat the target
	// on every step. A no-op when the trigger names no target, which is every
	// application that does not dispatch work to other machines.
	applyRunTarget(steps, trigger.Target, a.registry)

	// Filter unknown tools.
	valid := steps[:0]
	for _, s := range steps {
		if _, ok := a.registry.Get(s.Tool); ok {
			valid = append(valid, s)
		} else {
			log.Printf("[dag] executive hallucinated unknown tool %q, dropping step", s.Tool)
		}
	}
	if len(valid) == 0 {
		// Every planned tool was hallucinated — none exist in the registry.
		// That means the planner found no real tools to run —
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
			if err := validatePlanWiring(s.Tool, depStrs, s.Params); err != nil {
				log.Printf("[dag] executive plan validation: dropping step %q (%s): %v", s.Tag, s.Tool, err)
				continue
			}
			stillValid = append(stillValid, s)
		}
		valid = stillValid
		if len(valid) == 0 {
			return nil, fmt.Errorf("planner steps all failed data-flow validation — every compute/edit_file step had depends_on without ${step.N.field} wiring")
		}
	}
	result := &PlanResult{Steps: valid, WasAuto: isAuto}
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
				tool, ok := a.registry.Get(s.Tool)
				if !ok {
					continue
				}
				rank := a.intentRegistry.ResolveToolIntent(s.Tool, tool, s.Params)
				if rank > maxRank {
					maxRank = rank
				}
			}
			inferredIntent = gates.Intent(a.intentRegistry.SnapUp(maxRank))
		}
		result.InferredIntent = inferredIntent
		log.Printf("[dag] inferred intent: %s (from the impacts of the planned steps)", inferredIntent)
	}

	return result, nil
}

/*
 * parseExecutiveOutput extracts PlanSteps from the LLM's raw text output.
 * desc: Always expects a JSON array of steps. If the LLM wraps it in an object
 *       (e.g. {"intent":"...", "steps":[...]}), extracts the array from it.
 *       Validates deps and ${step.N} placeholder ranges, auto-adds missing dep
 *       step dependencies, detects and breaks cycles, and deduplicates wired
 *       steps.
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
				idx, ok := stepIndexFor(m[1], steps)
				if !ok || idx < 0 || idx >= len(steps) {
					log.Printf("[dag] step %d template ${step.%s...} names no step", i, m[1])
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
 *       one is found, the offending dependency is removed to break the cycle.
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
				// A dependency pointing back at a step still being walked is a cycle.
				// Drop it.
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
				if idx, found := stepIndexFor(m[1], steps); found {
					foundStep = idx
					foundField = m[2]
					ok = true
				}
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
				old, numeric := strconv.Atoi(m[1])
				if numeric != nil {
					// A tag, not a position. Tags are stable across a renumber —
					// that is the point of them — so this rewrite has nothing to
					// do. Atoi used to turn it into 0 and remap it to whatever
					// step 0 became.
					return match
				}
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
 *       reflection nodes between depth batches (reflect mode only).
 * param: steps - the parsed plan steps.
 * param: graph - the investigation graph.
 * param: budget - the execution budget.
 * param: registry - tool registry for source tagging and schema validation (optional).
 * param: dagMode - optional DAG mode override for reflection injection.
 * return: slice of created Node pointers, or error.
 */
func planStepsToNodes(steps []PlanStep, graph *Graph, budget *Budget, registry *toolapi.Registry, dagMode ...string) ([]*Node, error) {
	// Pass 1: create nodes and collect their graph IDs
	nodeIDs := make([]string, len(steps))
	nodes := make([]*Node, len(steps))

	for i, s := range steps {

		// At plan time, only check total node count — not per-tool batch limits.
		// Per-tool limits are for execution batching, not planning.
		// Pass "" for tool to skip batch counter; pass false for isLLM (tool node).
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
			// Carried from the plan so the dispatcher knows where to run it.
			// Empty means here, which is the case for every application that
			// never sets a target.
			Target: s.Target,
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
			// Never wire a node to itself. A replan frequently emits a stale
			// depends_on:[0] on its own FIRST step — a reference to a prior-frame
			// search that this plan-local index can't reach. Depending on itself makes the
			// node wait on itself, so it's cascaded to StateSkipped and the reflector
			// re-plans the same fetch forever (the observed web_fetch loop).
			if depIdx < len(nodeIDs) && nodeIDs[depIdx] != "" && nodeIDs[depIdx] != nodeIDs[i] {
				nodes[i].DependsOn = append(nodes[i].DependsOn, nodeIDs[depIdx])
			}
		}

		// Walk params, rewrite ${step.N...} → ${node.<id>...}, collect the
		// dependencies those references imply. The plan does not state them: a
		// step referencing another IS the dependency, and stating it twice is
		// how the two came to disagree. Logs each substitution for trace
		// visibility.
		extraDeps := rewriteStepTemplates(nodes[i].Params, nodeIDs, nodes[i].ID, steps, registry, graph)
		for _, dep := range extraDeps {
			if dep == nodes[i].ID {
				continue // never self-depend (defensive; rewriteStepTemplates also guards this)
			}
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

	// Batch reflections removed — the scheduler handles reflection timing.
	// Injecting reflections at plan time caused cascading debugger spawns
	// when early batches failed and all reflection nodes became ready at once.

	return nodes, nil
}

/*
 * nodeForRef turns the name in a reference into the node that holds the value.
 * desc: The plan being written first, which is what a name means when the
 *       planner just wrote it. Only when the plan has no such step does this
 *       reach back — positions restart with every plan, so a name is all that
 *       still points at work that already ran.
 *
 *       Reaching back is NOISY on purpose. It is right often enough to be worth
 *       doing: the planner has that step's result in front of it and is wiring
 *       to what it read. It is also how a plan that FORGOT to add a step still
 *       resolves — quietly reading the older value instead of producing a fresh
 *       one — and that is a wrong answer rather than a failure, so the line
 *       below is the only place a reader can see it happened.
 * param: name - the name the reference used.
 * param: nodeIDs - graph ids by position, for the plan being written.
 * param: steps - the plan being written.
 * param: graph - the run, or nil when there is none to reach back into.
 * param: owner - the node the reference is on, for the log line.
 * return: the node's id, and whether one was found.
 */
func nodeForRef(name string, nodeIDs []string, steps []PlanStep, graph *Graph, owner string) (string, bool) {
	if idx, ok := stepIndexFor(name, steps); ok && idx >= 0 && idx < len(nodeIDs) && nodeIDs[idx] != "" {
		return nodeIDs[idx], true
	}
	id, round, found := graph.FinishedStep(name)
	if !found {
		return "", false
	}
	log.Printf("[dag] warning: %s references %q, which is not a step in this plan — "+
		"resolved to %s from round %d. The value is the one that step already produced; "+
		"if fresh data was wanted, the plan is missing the step that produces it.",
		owner, name, id, round)
	return id, true
}

// stepTemplateRe matches ${step.N(.path)?} placeholders in param strings.
// Used at plan time to rewrite step indices to concrete node IDs and to
// validate field paths against upstream output schemas.
//
//	${step.0}                 → match, step=0, path=""
//	${step.3.content}         → match, step=3, path="content"
//	${step.0.results.0.url}   → match, step=0, path="results.0.url"
//	${node.X.field}           → not matched (already rewritten)
//
// A step reference names either a position in the plan or a step's tag. Tags
// matter because a re-plan renumbers positions and does not rename tags, so a
// reflector naming an earlier step reaches for the tag — the only form that
// still means the same step in the next round.
// A step's name may be written in any script. What it may not contain is a
// delimiter: the dot separates the name from the field, the brace closes the
// reference, and whitespace makes a name that reads as two.
//
// It was [a-zA-Z0-9_-], which is not a rule about references — it is a rule
// about English. A run held in Chinese names its steps in Chinese, and every
// one of them was silently unaddressable.
var stepNameRe = regexp.MustCompile(`^[\p{L}\p{N}_-]+$`)

var stepTemplateRe = regexp.MustCompile(`\$\{step\.([\p{L}\p{N}_-]+)(?:\.([^}]+))?\}`)

// availableStepNames lists what a reference COULD have named, for the message
// a planner is asked to correct against. A fault that says only what is wrong
// leaves the next attempt guessing.
func availableStepNames(steps []PlanStep, except int) string {
	var names []string
	for i := range steps {
		if i != except && steps[i].Tag != "" {
			names = append(names, strconv.Quote(steps[i].Tag))
		}
	}
	if len(names) == 0 {
		return "this plan has no other named steps"
	}
	return "the steps in this plan are named " + strings.Join(names, ", ")
}

// fieldNotProduced reports why a tool does not return a field, or "" when it
// does. Only a tool that DECLARES an output schema is checked, and only the
// first segment of the path — an incomplete deep schema must not reject a
// reference that would have worked.
func fieldNotProduced(toolName, field string, registry *toolapi.Registry) string {
	top := strings.SplitN(field, ".", 2)[0]
	if isNumericPathSegment(top) {
		return ""
	}
	tool, ok := registry.Get(toolName)
	if !ok {
		return ""
	}
	outSchema := toolapi.GetOutputSchema(tool)
	if outSchema == nil {
		return ""
	}
	if envelopeFieldExists(outSchema, top) || fieldExistsInSchema(outSchema, top) {
		return ""
	}
	return fmt.Sprintf("reads field %q, which %s does not return", top, toolName)
}

// stepIndexFor resolves the first segment of a step reference to a position.
// A number is a position. Anything else is a tag, looked up in the plan.
//
// It arrived with the reference pipeline and the rewriter, and three places in
// this file kept the assumption it was written to remove: strconv.Atoi with the
// error discarded, which turns every tag into step 0. So a reference meant for
// a fetch read the first step's output, the step that needed the page never got
// it, and the run re-planned and made the same reference again — or, where the
// step writing it was step 0, referenced itself. Everything that reads the
// first segment now comes through here.
func stepIndexFor(ref string, steps []PlanStep) (int, bool) {
	if n, err := strconv.Atoi(ref); err == nil {
		return n, true
	}
	for i := range steps {
		if steps[i].Tag == ref {
			return i, true
		}
	}
	return -1, false
}

// rewriteStepTemplates walks every string value reachable in params,
// finds ${step.N(.path)?} placeholders, and rewrites them in place to
// ${node.<id>(.path)?} using the plan's step-index → node-id mapping.
// Returns the set of node IDs referenced by templates so the caller can
// add them to depends_on if not already present (a common LLM omission).
//
// Field-path warnings against upstream tool output schemas are logged
// here too — the same diagnostic the param_refs code path used to emit,
// just keyed off the new template syntax.
func rewriteStepTemplates(params map[string]any, nodeIDs []string, owner string, steps []PlanStep, registry *toolapi.Registry, graph *Graph) []string {
	var implicitDeps []string
	walkParams(params, func(s string) (any, bool) {
		out := stepTemplateRe.ReplaceAllStringFunc(s, func(match string) string {
			m := stepTemplateRe.FindStringSubmatch(match)
			idx, resolved := stepIndexFor(m[1], steps)
			field := m[2]
			if !resolved {
				// Not a step being written. It may still name one that already
				// ran, in which case it points at a node that exists and holds
				// a value — see nodeForRef for why that is resolved rather than
				// refused, and why it says so out loud.
				if id, ok := nodeForRef(m[1], nodeIDs, steps, graph, owner); ok {
					implicitDeps = append(implicitDeps, id)
					return "${node." + id + dotPrefix(field) + "}"
				}
				log.Printf("[dag] template %s on %s names no step in this plan and no step this run ran, leaving placeholder unresolved", match, owner)
				return match
			}
			if idx < 0 || idx >= len(nodeIDs) || nodeIDs[idx] == "" || nodeIDs[idx] == owner {
				// Out of range, or a SELF-reference. A node can't consume its own
				// output, and a replan often points ${step.0…} at what is really a
				// prior-frame (concluded) node this plan-local index can't reach.
				// Leave the placeholder unresolved rather than wiring a dead/self
				// dependency — otherwise the node waits on itself, is skipped, and the
				// reflector re-plans the same fetch forever.
				log.Printf("[dag] template %s on %s references invalid/self step %d, leaving placeholder unresolved", match, owner, idx)
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
				if tool, ok := registry.Get(upstreamTool); ok {
					outSchema := toolapi.GetOutputSchema(tool)
					if outSchema == nil {
						log.Printf("[dag] warning: template on %s references %s which has no output schema", owner, upstreamTool)
					} else if !envelopeFieldExists(outSchema, strings.SplitN(field, ".", 2)[0]) &&
						!fieldExistsInSchema(outSchema, field) {
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

// stepRef is a ${step.N(.field)?} reference found in a plan step's params.
type stepRef struct {
	idx   int
	field string
	raw   string
}

// stepRefsIn collects every ${step.N(.path)?} reference in a step's params.
func stepRefsIn(params map[string]any, steps []PlanStep) []stepRef {
	var refs []stepRef
	walkParams(params, func(s string) (any, bool) {
		for _, m := range stepTemplateRe.FindAllStringSubmatch(s, -1) {
			idx, ok := stepIndexFor(m[1], steps)
			if !ok {
				// Named nothing. Carried as an impossible index so the caller
				// reports it rather than reading some other step's output.
				idx = -1
			}
			refs = append(refs, stepRef{idx: idx, field: m[2], raw: m[0]})
		}
		return s, false
	})
	return refs
}

// validatePlanReferences checks every ${step.N.field} reference against the plan
// and the producer tool's declared output, returning one human-readable error
// per broken reference (empty ⇒ clean). It catches the three ways the planner
// mis-wires a hand-off between steps — generically, for any tool, from the
// output schemas:
//
//   - SELF-reference: a step reads its own output (${step.i…} inside step i).
//   - OUT-OF-RANGE: a step reads a step index that isn't in the plan.
//   - WRONG PRODUCER: a step reads a top-level field the producing tool never
//     emits — classically ${step.N.results.0.url} off a web_fetch, when a
//     results[] list only comes from web_search.
//
// The executive turns these into ONE re-plan with the messages as feedback,
// rather than running a doomed plan: a self/out-of-range ref leaks its literal
// placeholder to the tool ("invalid URL …"), and a wrong-producer ref dangles a
// dead step dependency the reflector then re-plans forever. Only a tool that DECLARES an
// output schema is checked, and only its top-level field — so an incomplete deep
// schema can't cause a false reject.
/*
 * refNamesAFinishedStep reports whether a reference names a step this run
 * already ran.
 * desc: The name inside ${step.<name>…}. Reported so validation does not refuse
 *       a reference that wiring will resolve — the two disagreeing would fail a
 *       plan that was about to work.
 * param: raw - the reference as written.
 * param: graph - the run.
 * return: true when a finished step holds that name.
 */
func refNamesAFinishedStep(raw string, graph *Graph) bool {
	name := raw
	if m := stepTemplateRe.FindStringSubmatch(raw); m != nil {
		name = m[1]
	}
	_, _, found := graph.FinishedStep(name)
	return found
}

func validatePlanReferences(steps []PlanStep, registry *toolapi.Registry) []string {
	return validatePlanReferencesIn(steps, registry, nil)
}

// validatePlanReferencesIn is validatePlanReferences with the run, so a name
// that reaches a step which already ran is not reported as a fault.
func validatePlanReferencesIn(steps []PlanStep, registry *toolapi.Registry, graph *Graph) []string {
	var errs []string

	for i, s := range steps {
		for _, r := range stepRefsIn(s.Params, steps) {
			switch {
			case r.idx == i:
				errs = append(errs, fmt.Sprintf("step %d (%s): %s references its OWN output — a step cannot consume itself; reference an EARLIER step, or add the step that produces this value", i, s.Tool, r.raw))
			case r.idx < 0 && graph != nil && refNamesAFinishedStep(r.raw, graph):
				// Resolvable: it names a step this run already ran. Wiring says
				// so at plan time; the loudness lives there, not here.
			case r.idx < 0:
				errs = append(errs, fmt.Sprintf("step %d (%s): %s names no step in THIS plan. "+
					"If it is a step from earlier in the run, its value is already above as a tool result — "+
					"copy the value itself into the param. A reference reaches steps in this plan only. "+
					"Otherwise use a step in this plan, by its exact tag — %s",
					i, s.Tool, r.raw, availableStepNames(steps, i)))
			case r.idx < 0 || r.idx >= len(steps):
				errs = append(errs, fmt.Sprintf("step %d (%s): %s points at step %d, which does not exist (the plan has %d steps)", i, s.Tool, r.raw, r.idx, len(steps)))
			case r.field != "" && registry != nil:
				top := strings.SplitN(r.field, ".", 2)[0]
				if isNumericPathSegment(top) {
					continue
				}
				producer := steps[r.idx].Tool
				tool, ok := registry.Get(producer)
				if !ok {
					continue
				}
				outSchema := toolapi.GetOutputSchema(tool)
				if outSchema == nil {
					continue
				}
				// Tool outputs are wrapped in a uniform envelope {kind,status,content,
				// data:…}. A ${step.N.field} reference resolves against the unwrapped
				// payload, so a field can sit at the envelope's top level (content,
				// status) OR inside its data (e.g. web_search's results[]). Accept
				// either — checking only the top level wrongly rejects `results`, which
				// with the hard-fail loop kills every valid search→fetch plan.
				if envelopeFieldExists(outSchema, top) {
					continue
				}
				if fieldExistsInSchema(outSchema, top) {
					continue
				}
				// Write down what this check saw before rejecting. The message
				// alone cannot distinguish "the field is absent" from "the
				// validator and the resolver disagree about it".
				logRejectedReference(r.raw, top, producer, outSchema)

				hint := ""
				if top == "results" {
					hint = fmt.Sprintf(" — a results[] list of URLs comes from web_search, not %s; add a web_search step and reference ITS results", producer)
				}
				errs = append(errs, fmt.Sprintf("step %d (%s): %s reads field %q from step %d (%s), but %s does not produce %q%s", i, s.Tool, r.raw, top, r.idx, producer, producer, top, hint))
			}
		}
	}
	return errs
}

// validatePlanParams checks each step's parameters against its tool's own input
// schema, returning one human-readable error per fault (empty ⇒ clean). It is the
// plan-time twin of validatePlanReferences: that checks the wiring BETWEEN steps
// (${step.N} references), this checks the parameters OF a step. Two faults:
//
//   - a name the schema does not declare — web_search(topn: …), where the count
//     knob is max_results. Checked only when the schema is closed
//     (additionalProperties:false); an open-schema tool (compute, edit_file)
//     legitimately takes extra dotted context.* keys, so extras are allowed there.
//   - a name the schema marks required that the step does not supply. Checked for
//     every tool, open schema or closed, because requiring a parameter and
//     allowing unlisted ones are separate statements. A step supplying none at all
//     is checked too: no parameters is the largest possible omission, not a reason
//     to skip.
//
// Both come back as feedback the executive re-plans against, instead of the call
// being rejected later at dispatch, or — where the schema is open and the tool
// reads a default — the step running and answering a question nobody asked. The
// name and requirement sets come from each tool's OWN schema, so this is not a
// hardcoded list and covers any tool, not specific ones.
func validatePlanParams(steps []PlanStep, registry *toolapi.Registry) []string {
	if registry == nil {
		return nil
	}
	var errs []string
	for i, s := range steps {
		tool, ok := registry.Get(s.Tool)
		if !ok {
			continue // unknown tool name — not this check's job
		}
		schema, err := parseToolSchema(tool.Parameters())
		if err != nil {
			continue // unreadable schema → nothing to check against
		}
		for _, m := range missingRequiredParams(schema, s.Params) {
			if m.When != "" {
				errs = append(errs, fmt.Sprintf("step %d (%s): required parameter %q is not supplied — %s requires it when %s",
					i, s.Tool, m.Name, s.Tool, m.When))
				continue
			}
			errs = append(errs, fmt.Sprintf("step %d (%s): required parameter %q is not supplied — %s requires: %s",
				i, s.Tool, m.Name, s.Tool, strings.Join(schema.Required, ", ")))
		}
		if schema.AdditionalProperties {
			continue // extras allowed → no name left to reject
		}
		for key := range s.Params {
			if _, declared := schema.Properties[key]; declared {
				continue
			}
			errs = append(errs, fmt.Sprintf("step %d (%s): parameter %q does not exist — %s accepts only: %s",
				i, s.Tool, key, s.Tool, strings.Join(sortedKeys(schema.Properties), ", ")))
		}
	}
	return errs
}

// envelopeData extracts the "data" sub-schema from a tool's envelope output
// schema, or nil if absent. Tool results are wrapped as {kind,status,content,
// data:<tool payload>}; a ${step.N.field} reference resolves against the
// unwrapped payload, so a field like web_search's "results" lives under data,
// not at the envelope's top level — field checks must look here too.
// envelopeFieldExists reports whether the envelope ITSELF declares a field —
// content, status, type, detail — without unwrapping to the payload.
//
// It exists because the check beside it does unwrap, deliberately, since most
// references name a payload field. Both were being called here to mean "the
// envelope's own level, or inside its data", and because both unwrapped they
// asked the same question twice: every envelope field was rejected at plan
// time, `content` among them.
//
// `content` is the rendered text of every tool's result and the most common
// hand-off there is — the planner's own prompt teaches file_read → compute
// wired on ${step.0.content}. That plan is correct, the dispatcher resolves it
// (see resolveTemplateField, which walks the real JSON and finds it), and this
// validator threw it out. The planner then spent its three corrections being
// told to fix a plan that was right, and ended up dropping steps until one
// referenced itself.
func envelopeFieldExists(schemaJSON json.RawMessage, field string) bool {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(schemaJSON, &s) != nil {
		return false
	}
	_, ok := s.Properties[field]
	return ok
}

func envelopeData(schemaJSON json.RawMessage) json.RawMessage {
	var s struct {
		Properties struct {
			Data json.RawMessage `json:"data"`
		} `json:"properties"`
	}
	if json.Unmarshal(schemaJSON, &s) != nil {
		return nil
	}
	return s.Properties.Data
}

/*
 * fieldExistsInSchema checks if a dot-path field exists in a JSON Schema's properties.
 * desc: Used to validate template field paths against declared output schemas
 *       at plan time. Supports nested objects and array items traversal.
 * param: schemaJSON - the raw JSON Schema bytes.
 * param: fieldPath - dot-separated field path to validate.
 * return: true if the field path exists in the schema.
 */
func fieldExistsInSchema(schemaJSON json.RawMessage, fieldPath string) bool {
	// A reference names a field of the tool's payload, so it is checked against
	// the payload's schema. Checking the envelope's instead warned that every
	// correct reference was to a field the tool does not have.
	schemaJSON = toolapi.PayloadSchema(schemaJSON)
	if schemaJSON == nil {
		return false
	}
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

/*
 * applyRunTarget propagates a run's target onto the steps that need one.
 * desc: A plan is written by a model that may or may not repeat the target on
 *       every step. Rather than rely on it, fill in what the tool declares it
 *       needs: a step whose tool requires a target inherits the run's, and a
 *       step whose tool does not is stripped of one it should never have had.
 *
 *       A step needing a target when the run has none is left alone and
 *       logged. It is not quietly run here — a tool that must name a machine
 *       has no sensible default, and running it locally would produce a
 *       plausible answer about the wrong host.
 * param: steps - the plan, modified in place.
 * param: target - the run's target, or "" when it has none.
 * param: registry - used to read each tool's target requirement.
 */
func applyRunTarget(steps []PlanStep, target string, registry *toolapi.Registry) {
	if registry == nil {
		return
	}
	for i := range steps {
		tool, ok := registry.Get(steps[i].Tool)
		if !ok {
			continue
		}
		if toolapi.RequiresTarget(tool) {
			if steps[i].Target != "" {
				continue
			}
			if target == "" {
				log.Printf("[plan] step %d (%s) needs a target machine but the run has none; leaving it unset rather than running it here",
					i, steps[i].Tool)
				continue
			}
			steps[i].Target = target
			continue
		}
		if steps[i].Target != "" {
			log.Printf("[plan] step %d (%s) does not take a target; clearing %q",
				i, steps[i].Tool, steps[i].Target)
			steps[i].Target = ""
		}
	}
}

/*
 * budgetLine states the plan's limits to the planner.
 * desc: Named separately so the wording can be asserted; a bare "max N steps"
 *       was read as an amount to spend rather than a limit to stay under.
 */
func budgetLine(maxNodes, maxLLMCalls int) string {
	return fmt.Sprintf("\nBudget: at most %d steps and %d LLM calls. This is a ceiling, not a target — plan only the steps the objective needs, and stop there. Do not add steps that examine something the objective did not name.\n", maxNodes, maxLLMCalls)
}
