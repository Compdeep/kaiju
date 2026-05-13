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
	"strings"

	"github.com/Compdeep/kaiju/internal/agent/tools"
)

// parsedSchema is the minimal JSON-Schema shape the validator needs.
// Full schema support is unnecessary — we only care whether a given
// param name is allowed.
type parsedSchema struct {
	Properties           map[string]json.RawMessage
	AdditionalProperties bool // default true (JSON Schema), set false only when schema says so
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
		AdditionalProperties json.RawMessage            `json:"additionalProperties"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return parsedSchema{}, fmt.Errorf("tool schema unreadable: %w", err)
	}
	out := parsedSchema{
		Properties:           aux.Properties,
		AdditionalProperties: true, // JSON Schema default
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

// validateDirectParams rejects any key in `params` that the tool's
// schema does not allow. Templates inline (`${node.X.field}`) are just
// strings as far as this check is concerned — the schema either lists
// the param name or it doesn't.
//
// Returns nil when every key is allowed. Returns a descriptive error
// naming the first offending key and the tool's allowed set when not.
func validateDirectParams(tool tools.Tool, params map[string]any) error {
	schema, err := parseToolSchema(tool.Parameters())
	if err != nil {
		return fmt.Errorf("validate %s params: %w", tool.Name(), err)
	}
	if schema.AdditionalProperties {
		return nil // tool's schema allows extras; nothing to reject
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
func paramsContainTemplate(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for _, val := range x {
			if s, ok := val.(string); ok {
				if strings.Contains(s, "${node.") || strings.Contains(s, "${step.") {
					return true
				}
			} else if paramsContainTemplate(val) {
				return true
			}
		}
	case []any:
		for _, val := range x {
			if s, ok := val.(string); ok {
				if strings.Contains(s, "${node.") || strings.Contains(s, "${step.") {
					return true
				}
			} else if paramsContainTemplate(val) {
				return true
			}
		}
	}
	return false
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
