// Package agent — dispatcher_validation.go.
//
// Pure validation helpers for the dispatcher. Stateless and
// unit-testable without a graph or dispatcher fixture.
//
//   validateDirectParams — catches planner-invented param names on a
//     tool call before it runs (e.g. bash({command, cwd}) — bash has
//     no cwd). Called from fireNode just before executeToolNode.
//
//   validateDataFlow — catches the omission case: compute / edit_file
//     declares depends_on for upstream steps but wires NO ${node...}
//     templates anywhere in params. Means the planner depended on a
//     step's data and forgot to interpolate it.
//
// Each failure is logged with a [dispatch:reject] prefix so traces can
// count rejection rates without a dedicated metric.
//
// Compute is not special-cased here. Its schema doesn't set
// `additionalProperties: false`, so JSON Schema default (true) applies
// and validateDirectParams allows extras (the dotted "context.foo" key
// pattern compute uses for context injection). Bash explicitly sets
// `additionalProperties: false`, so its extras get rejected. Each tool
// declares its own strictness; the validator just reads.

package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// parsedSchema is the minimal JSON-Schema shape the validator needs.
// Full schema support is unnecessary — we only care whether a given
// param name is allowed.
type parsedSchema struct {
	Properties           map[string]json.RawMessage
	Required             []string                 // names the schema marks required, in declared order
	Conditional          []conditionalRequirement // names required only in certain modes
	AdditionalProperties bool                     // default true (JSON Schema), set false only when schema says so
}

// conditionalRequirement is one "when this parameter holds one of these values,
// these other parameters are required" rule, read from a schema's allOf entries.
// Tools whose behaviour changes with a mode carry rules the flat "required" list
// cannot express: clipboard needs content only to write, service needs command
// only to start. Written flat, those parameters could not be marked required at
// all, because they are not required in every mode — so the rule lived in a
// description string, where no check could reach it and the call failed part-way
// through a run instead of at planning.
//
// Only one shape is read, and anything else in allOf is ignored rather than
// guessed at:
//
//	{"if":   {"properties": {"action": {"const": "write"}}, "required": ["action"]},
//	 "then": {"required": ["content"]}}
//
// "enum" stands in for "const" when several values share a rule.
type conditionalRequirement struct {
	When    string   // the parameter whose value selects the mode
	Equals  []string // the values of that parameter which make the rule apply
	Require []string // the parameters required when it does
}

// describe names the rule in the words the planner is given back.
func (c conditionalRequirement) describe() string {
	if len(c.Equals) == 1 {
		return fmt.Sprintf("%s is %q", c.When, c.Equals[0])
	}
	quoted := make([]string, len(c.Equals))
	for i, v := range c.Equals {
		quoted[i] = strconv.Quote(v)
	}
	return fmt.Sprintf("%s is one of %s", c.When, strings.Join(quoted, ", "))
}

// applies reports whether params select a mode this rule governs. A discriminator
// that is absent, empty, not a string, or still an unresolved reference cannot be
// read, so the rule stays silent — a check that cannot tell must not reject.
func (c conditionalRequirement) applies(params map[string]any) bool {
	v, ok := params[c.When].(string)
	if !ok {
		return false
	}
	v = strings.TrimSpace(v)
	if v == "" || strings.Contains(v, "${") {
		return false
	}
	for _, want := range c.Equals {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

// parseToolSchema reads a tool's Parameters() output and extracts just
// the two fields that govern param-name validation. Returns an error if
// the JSON is malformed — the dispatcher treats that as a tool bug and
// fails the step loudly.
func parseToolSchema(raw json.RawMessage) (parsedSchema, error) {
	if len(raw) == 0 {
		// A tool declaring no schema at all is lenient by default —
		// same as `{}` which has no properties and no restriction.
		return parsedSchema{AdditionalProperties: true}, nil
	}
	// We decode into an intermediate with AdditionalProperties as
	// json.RawMessage so we can distinguish "absent" (default true) from
	// "explicit true" / "explicit false" / "object form" (treated as
	// true — we don't support conditional-properties schemas here).
	var aux struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties json.RawMessage            `json:"additionalProperties"`
		AllOf                []json.RawMessage          `json:"allOf"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return parsedSchema{}, fmt.Errorf("tool schema unreadable: %w", err)
	}
	out := parsedSchema{
		Properties:           aux.Properties,
		Required:             aux.Required,
		AdditionalProperties: true, // JSON Schema default
	}
	for _, entry := range aux.AllOf {
		if c, ok := parseConditional(entry); ok {
			out.Conditional = append(out.Conditional, c)
		}
	}
	if len(aux.AdditionalProperties) > 0 {
		var b bool
		if err := json.Unmarshal(aux.AdditionalProperties, &b); err == nil {
			out.AdditionalProperties = b
		}
		// Non-bool additionalProperties (object form) falls through as
		// the default `true` — we don't support sub-schemas.
	}
	return out, nil
}

// parseConditional reads one allOf entry in the single supported shape, and
// reports false for anything else — an entry using a construct this does not
// model is skipped, never guessed at, because a misread rule would reject a call
// that works.
func parseConditional(raw json.RawMessage) (conditionalRequirement, bool) {
	var entry struct {
		If struct {
			Properties map[string]struct {
				Const json.RawMessage `json:"const"`
				Enum  []string        `json:"enum"`
			} `json:"properties"`
		} `json:"if"`
		Then struct {
			Required []string `json:"required"`
		} `json:"then"`
	}
	if json.Unmarshal(raw, &entry) != nil {
		return conditionalRequirement{}, false
	}
	// One discriminator per rule. Two would mean a combination this does not model.
	if len(entry.Then.Required) == 0 || len(entry.If.Properties) != 1 {
		return conditionalRequirement{}, false
	}
	for key, cond := range entry.If.Properties {
		values := cond.Enum
		if len(cond.Const) > 0 {
			var one string
			if json.Unmarshal(cond.Const, &one) == nil {
				values = append(values, one)
			}
		}
		if len(values) == 0 {
			return conditionalRequirement{}, false
		}
		return conditionalRequirement{When: key, Equals: values, Require: entry.Then.Required}, true
	}
	return conditionalRequirement{}, false
}

// missingRequirement is one parameter a call does not supply that its tool's
// schema demands, carrying the mode that demanded it when a conditional rule is
// the source — the planner needs to know a parameter became required because of
// the mode it chose, not merely that it is missing.
type missingRequirement struct {
	Name string
	When string // empty when the schema requires this in every mode
}

func (m missingRequirement) String() string {
	if m.When == "" {
		return strconv.Quote(m.Name)
	}
	return fmt.Sprintf("%s (required when %s)", strconv.Quote(m.Name), m.When)
}

// missingRequiredParams returns the names of parameters the schema marks
// required that `params` carries no usable value for, in the order the
// schema lists them. A key that is absent, nil, or a string that is empty
// once trimmed counts as missing; false and 0 are values, not absences.
//
// An unresolved reference (`${step.2.path}`) counts as supplied — whether
// it resolves is resolution's business, not this check's.
func missingRequiredParams(schema parsedSchema, params map[string]any) []missingRequirement {
	var missing []missingRequirement
	reported := make(map[string]bool)
	add := func(key, when string) {
		if reported[key] || suppliedParam(params, key, schema.allowsEmpty(key)) {
			return
		}
		reported[key] = true
		missing = append(missing, missingRequirement{Name: key, When: when})
	}
	for _, key := range schema.Required {
		add(key, "")
	}
	for _, c := range schema.Conditional {
		if !c.applies(params) {
			continue
		}
		for _, key := range c.Require {
			add(key, c.describe())
		}
	}
	return missing
}

// suppliedParam reports whether params carries a usable value for key. Absent
// or nil is never usable; false and 0 are values a caller chose, not absences.
//
// An empty string is the one case the caller decides, via allowEmpty. For most
// parameters an empty string is a planner that left the value blank — a search
// with no query, a command with no command — and treating it as missing gets a
// correction instead of a step that runs and answers nothing. But for a few it
// is the value: file_write with content "" creates an empty file, and truncates
// one. There the blanket rule is unwinnable, because the correction asks for a
// value the planner already supplied and the only other move — inventing file
// contents — its own prompt forbids. The schema says which kind a parameter is;
// see parsedSchema.allowsEmpty.
//
// Whitespace still trims to empty, so allowEmpty also admits content that is
// exactly a newline — a real file, and one the trimming rule alone rejected.
func suppliedParam(params map[string]any, key string, allowEmpty bool) bool {
	v, present := params[key]
	if !present || v == nil {
		return false
	}
	if str, isStr := v.(string); isStr && strings.TrimSpace(str) == "" {
		return allowEmpty
	}
	return true
}

// allowsEmpty reports whether the schema explicitly permits an empty string for
// key, by declaring "minLength": 0 on that property. Opt-in and per-parameter:
// a property that says nothing keeps the default, so this widens only the
// parameters a tool author has marked, and never a whole tool.
func (s parsedSchema) allowsEmpty(key string) bool {
	raw, ok := s.Properties[key]
	if !ok {
		return false
	}
	var prop struct {
		MinLength *int `json:"minLength"`
	}
	if json.Unmarshal(raw, &prop) != nil || prop.MinLength == nil {
		return false
	}
	return *prop.MinLength == 0
}

// names reduces the requirements to bare parameter names, for messages that list
// what a tool wants rather than what one call left out.
func names(reqs []missingRequirement) []string {
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.Name
	}
	return out
}

// validateDirectParams rejects a call the tool's own schema says is wrong:
// a key the schema does not allow, or a key the schema marks required that
// the call does not supply. Templates inline (`${node.X.field}`) are just
// strings as far as this check is concerned — the schema either lists the
// param name or it doesn't.
//
// The required check runs whatever the schema says about extras, because
// requiring a parameter and allowing unlisted ones are separate statements;
// the unknown-key check runs only on a closed schema. This is the dispatch-time
// backstop for validatePlanParams, which catches both at plan time where the
// executive can re-plan against them.
//
// Returns nil when the call is allowed. Returns a descriptive error naming
// the first offending key and the tool's own set when not.
func validateDirectParams(tool toolapi.Tool, params map[string]any) error {
	schema, err := parseToolSchema(tool.Parameters())
	if err != nil {
		return fmt.Errorf("validate %s params: %w", tool.Name(), err)
	}
	if missing := missingRequiredParams(schema, params); len(missing) > 0 {
		log.Printf("[dispatch:reject] %s: required param(s) not supplied: %s",
			tool.Name(), strings.Join(names(missing), ", "))
		return fmt.Errorf("tool %s rejected: required parameter %s not supplied",
			tool.Name(), missing[0])
	}
	if schema.AdditionalProperties {
		return nil // tool's schema allows extras; no name left to reject
	}
	for key := range params {
		if _, declared := schema.Properties[key]; declared {
			continue
		}
		allowed := sortedKeys(schema.Properties)
		log.Printf("[dispatch:reject] %s: unknown param %q (allowed: %s)",
			tool.Name(), key, strings.Join(allowed, ", "))
		return fmt.Errorf("tool %s rejected param %q — not in schema (allowed: %s)",
			tool.Name(), key, strings.Join(allowed, ", "))
	}
	return nil
}

// validateDataFlow rejects compute / edit_file nodes that declare
// depends_on but contain NO ${node...} templates anywhere in their
// params. The planner almost never depends a compute on upstream steps
// for pure sequencing — when it sets depends_on, it wants their data.
// Silence between "declared the dep" and "wrote a placeholder for it"
// is an omission bug: the compute would run without the data and
// hallucinate from the goal text or training memory.
//
// Scope: compute + edit_file only. Other tools (bash, service, etc.)
// legitimately use depends_on for ordering side-effects with no data
// flow — those aren't caught by this check. edit_file with task_files
// is also exempt — task_files IS the data source (the Coder reads each
// listed file directly), so depends_on against an upstream file_read
// is just harmless sequencing.
func validateDataFlow(toolName string, dependsOn []string, params map[string]any) error {
	if len(dependsOn) == 0 {
		return nil
	}
	if toolName != "compute" && toolName != "edit_file" {
		return nil
	}
	if toolName == "edit_file" && hasNonEmptyTaskFiles(params) {
		return nil
	}
	if paramsContainTemplate(params) {
		return nil
	}
	log.Printf("[dispatch:reject] %s: depends_on %v but no ${node...} templates — data flow incomplete",
		toolName, dependsOn)
	return fmt.Errorf("tool %s declares depends_on %v but no ${step.N.field} placeholder appears anywhere in params — if you depend on those steps' data, reference it inline (e.g. \"context.csv\": \"${step.0.content}\"); if you meant pure sequencing, compute/edit_file is the wrong tool",
		toolName, dependsOn)
}

// paramsContainTemplate reports whether any string-typed leaf reachable
// from v contains a ${node...} placeholder. Mirror of walkParams in
// dispatcher.go; kept local here so the validator stays a pure helper.
// paramsContainTemplate reports whether the params carry a reference to another
// step's output.
//
// Through the reference model rather than a substring scan. A scan for "${node."
// accepts a placeholder that is only half written — the step passes validation,
// nothing resolves it at fire time because the pattern does not match, and the
// tool is handed the literal text. Asking what references are actually there
// answers the question the caller meant.
func paramsContainTemplate(v any) bool {
	return len(FindRefs(v)) > 0
}

// hasNonEmptyTaskFiles reports whether params["task_files"] is a non-empty
// list. Accepts both []string (programmatic callers) and []any (JSON-decoded
// LLM output). Used by validateDataFlow to exempt edit_file from the
// "depends_on requires templates" rule when task_files supplies the data.
func hasNonEmptyTaskFiles(params map[string]any) bool {
	tf, ok := params["task_files"]
	if !ok || tf == nil {
		return false
	}
	switch v := tf.(type) {
	case []string:
		return len(v) > 0
	case []any:
		return len(v) > 0
	}
	return false
}

// What bounds a tool result, and where.
//
// Five caps sit between a tool and the model, and they are applied in this
// order. The number below is only the second of them, and for most tools it
// does not apply at all.
//
//  1. The TOOL's own cap, inside its Execute. web_fetch cuts a document at
//     16000 characters, a reader plugin's markdown at 12000, a raw body at
//
//  8192. These bound what goes into the envelope and are the only ones a
//     tool author controls.
//
//  2. toolResultBudget, applied by this function when a step finishes.
//     It bounds ONE step's result. On the ReAct path it applies to every tool.
//     On the DAG path it applies only to a tool that returned a plain string:
//     a tool implementing TypedExecutor takes the typed branch, which sets
//     isContextual and skips this — see dispatcher.go.
//
//  3. evidenceBudget, applied when results are assembled into a prompt. It
//     bounds ONE step's contribution to the evidence, not the whole. A run with
//     twenty steps sends twenty of these.
//
//  4. The context gate's budget, defaultMaxBudget 32000 unless a caller sets
//     MaxBudget. This one bounds the WHOLE prompt rather than one step, and it
//     trims by source priority — so it can cut a source the three above left
//     alone, or drop it entirely when what is left would be under gateMinChunk.
//     Its marker names itself; see gateTruncMarker in contextgate.go.
//
//  5. Text.TruncateLog, 500, on the audit line and the log. That one is a
//     record of what happened and never reaches a model.
//
// Each marker names the cap that made it, so a cut in a prompt can be traced to
// the number that caused it rather than guessed at.
//
// So a tool author raising its own cap from 8192 usually sees no change: for a
// typed tool the next thing to cut is 8000 at synthesis, and for a string tool
// it is 4096 here. Neither is in the tool's file, and both are ahead of the
// gate's 32000, which is about the whole prompt rather than this result.
//
// The number itself lives in budgets.go as toolResultBudget, with the other
// three caps on content, so a change to one is visibly a change relative to the
// rest. It was declared in loop_react.go and read by the DAG — one execution
// mode's constant governing the other, with nothing saying so.

// truncateToolResult caps an oversized tool Result while preserving
// JSON structure when the Result is itself JSON. Byte-splicing (old
// HeadTail) corrupts JSON by cutting mid-string — a downstream
// param_ref trying to extract a field then fails to parse the envelope
// at all. This function:
//
//  1. If the Result parses as a top-level JSON object, walks its string
//     fields and truncates the longest one down with a marker until the
//     total serialised size fits under the cap. The JSON stays valid.
//  2. Otherwise (plain text, malformed, non-object JSON), falls back to
//     the original head+tail splice, which is fine for LLM consumption
//     of unstructured output.
//
// Cap is a soft target — we stop truncating fields when we're close
// enough rather than iterating exactly.
func truncateToolResult(s string, cap int, headTail func(string, int, int, ...string) string) string {
	if len(s) <= cap {
		return s
	}
	// Try to parse as a top-level JSON object. Arrays / primitives fall
	// through to head+tail since they're rare for tool Result shapes.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return headTail(s, cap*2/3, cap/3,
			"\n... (middle truncated at cap; use start_line, grep, or tail to read the missing portion) ...\n")
	}
	// Shrink the largest string field(s) iteratively until under cap.
	const marker = "\n... (content truncated to fit result cap; rerun with narrower focus or use file_read on the source) ...\n"
	for len(mustMarshal(obj)) > cap {
		biggestKey := ""
		biggestLen := 0
		for k, v := range obj {
			var sv string
			if json.Unmarshal(v, &sv) != nil {
				continue // not a string — skip
			}
			if len(sv) > biggestLen {
				biggestLen = len(sv)
				biggestKey = k
			}
		}
		if biggestKey == "" || biggestLen < 256 {
			// No more long string fields to cut; fall back.
			return headTail(s, cap*2/3, cap/3, marker)
		}
		var cur string
		_ = json.Unmarshal(obj[biggestKey], &cur)
		// Head+tail the content field itself, preserving context from
		// start (headers, intro) and end (conclusions, errors).
		shrunk := headTail(cur, biggestLen/2, biggestLen/4, marker)
		if len(shrunk) >= len(cur) {
			// Couldn't reduce further with this strategy; bail out.
			return headTail(s, cap*2/3, cap/3, marker)
		}
		re, _ := json.Marshal(shrunk)
		obj[biggestKey] = re
	}
	return mustMarshal(obj)
}

// mustMarshal serialises a JSON object to a string, returning empty on
// error. Used inside truncateToolResult where the object was produced
// by a successful Unmarshal so re-Marshal should not fail.
func mustMarshal(obj map[string]json.RawMessage) string {
	b, err := json.Marshal(obj)
	if err != nil {
		return ""
	}
	return string(b)
}

// sortedKeys returns map keys in sorted order for deterministic error
// messages and log lines.
func sortedKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// wholeBesideExcerpt widens a reference to a truncated field into an object
// carrying that text, the file holding all of it, and that file's size.
//
// A tool cuts a value down to what fits a prompt and writes the whole of it to a
// file. Both are correct, and both are returned — but a ${step.N.field}
// reference names one field, so a plan that names the truncated one hands the
// next step a part and nothing else. That is how a step counted inside 8,102 of
// 1,219,043 characters and reported the number as the document's, three runs
// running, while the whole copy sat unread.
//
// Handing over both costs nothing: a step that only needed to read still has the
// text, and a step that has to work over everything now has somewhere to read it
// from. The keys are the tool's OWN declared names, so the engine imposes no
// vocabulary of its own beyond "note", which carries the tool's wording.
//
// The pairing is read from the tool through toolapi.Excerpting, never from a
// list kept here. A tool that declares nothing is treated as returning
// everything it produced, and its references resolve untouched.
//
// It widens only when the tool declared this field, the payload names a whole
// copy, and that copy is larger than the value resolved. A page that came back
// whole, a tool that kept no file, an unstated size, and a value that is not
// text are all left exactly as they were.
func wholeBesideExcerpt(reg *toolapi.Registry, graph *Graph, depID, field string, val any) (map[string]any, bool) {
	part, isText := val.(string)
	if !isText || graph == nil || reg == nil {
		return nil, false
	}
	producer := graph.Get(depID)
	if producer == nil || producer.Body == nil || producer.ToolName == "" {
		return nil, false
	}
	tool, known := reg.Get(producer.ToolName)
	if !known {
		return nil, false
	}
	for _, declared := range toolapi.GetExcerpts(tool) {
		if declared.Field != field {
			continue
		}
		rawWhole, _ := producer.Body.Field(declared.Whole)
		whole, _ := rawWhole.(string)
		if strings.TrimSpace(whole) == "" {
			return nil, false // the tool kept no file, so the text is all there is
		}
		widened := map[string]any{
			declared.Field: part,
			declared.Whole: whole,
		}
		switch {
		case declared.Flag != "":
			cut, _ := producer.Body.Field(declared.Flag)
			wasCut, _ := cut.(bool)
			if !wasCut {
				return nil, false // the tool says it returned everything
			}
			widened[declared.Flag] = true
		case declared.Size != "":
			total, stated := asByteCount(producer.Body.Field(declared.Size))
			if !stated || total <= len(part) {
				return nil, false // came back whole, or the size is not stated
			}
			widened[declared.Size] = total
		default:
			return nil, false // nothing declared to tell whether it was cut
		}
		if strings.TrimSpace(declared.Use) != "" {
			widened["note"] = declared.Use
		}
		return widened, true
	}
	return nil, false
}

// asByteCount reads a size out of a payload. A number that survived a JSON round
// trip arrives as float64, so both forms are accepted and anything else is
// treated as unstated rather than guessed at.
func asByteCount(v any, found bool) (int, bool) {
	if !found {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
