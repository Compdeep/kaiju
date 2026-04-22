// Package agent — dispatcher_validation.go.
//
// Pure validation helpers for the dispatcher. Two entry points, both
// stateless and unit-testable without a graph or dispatcher fixture:
//
//   validateDirectParams — catches planner-invented param names on a tool
//     call before it runs (e.g. bash({command, cwd}) — bash has no cwd).
//     Called from fireNode just before executeToolNode.
//
//   validateParamRef — catches param_ref wiring that names a field the
//     dep's Result does not contain (e.g. {step: 1, field: "output"} on
//     a compute that produced no output). Called from resolveInjections
//     after the dep has resolved, before the value is substituted.
//
// Each failure is logged with a [dispatch:reject] prefix so the two
// classes can be counted in traces without a dedicated metric.
//
// Compute is not special-cased here. It legitimately accepts arbitrary
// param keys as context via param_refs. Its schema doesn't set
// `additionalProperties: false`, so JSON Schema default (true) applies
// and validateDirectParams allows extras. Bash explicitly sets
// `additionalProperties: false`, so extras get rejected. Each tool
// declares its own strictness via its schema; the validator just reads.

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

// validateDirectParams rejects any key in `params` that the tool's schema
// does not allow. Keys injected via `refs` are excluded from the check —
// those get their own validation in validateParamRef at the injection
// site, and the planner legitimately chooses their names.
//
// Returns nil when every key is allowed. Returns a descriptive error
// naming the first offending key and the tool's allowed set when not.
func validateDirectParams(tool tools.Tool, params map[string]any, refs map[string]ResolvedInjection) error {
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
		if _, injected := refs[key]; injected {
			// Key came from param_refs — the name was the planner's
			// choice, not meant to match the schema. That class is
			// checked separately by validateParamRef.
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

// validateParamRef checks the param_ref against the dep's resolved
// Result. Two distinct failure shapes are handled differently:
//
//  1. dep.Result doesn't parse as JSON at all → this is an upstream tool
//     serialization bug, not a planner error. Returns the sentinel
//     errResultNotJSON so the caller (resolveInjections) can fall back
//     to injecting the full result as a string. Preserves legacy
//     tolerance for tool bugs.
//
//  2. dep.Result parses as valid JSON but the named field is missing →
//     this is a planner error (wired a field that doesn't exist, the
//     brace_json bug shape). Returns a descriptive error; caller fails
//     the step loudly.
//
// Empty ref.Field means "use the whole result as-is" — legitimate
// choice (chaining raw file_read output). Always accepted.
func validateParamRef(paramName string, ref ResolvedInjection, depResult string) error {
	if ref.Field == "" {
		return nil
	}
	// Try parsing the dep's Result. If it isn't JSON at all, that's a
	// tool-output bug — surface the sentinel so the caller can degrade
	// gracefully.
	var probe any
	if err := json.Unmarshal([]byte(depResult), &probe); err != nil {
		return errResultNotJSON
	}
	// Valid JSON envelope — now check the specific field.
	if _, err := extractJSONField(depResult, ref.Field); err != nil {
		log.Printf("[dispatch:reject] param_ref %q: field %q absent in dep %s (%v)",
			paramName, ref.Field, ref.NodeID, err)
		return fmt.Errorf("param_ref %q: field %q absent in dep %s (%w)",
			paramName, ref.Field, ref.NodeID, err)
	}
	return nil
}

// errResultNotJSON signals that validateParamRef couldn't parse the
// dep's Result as JSON at all — as opposed to the field being absent
// from a valid JSON envelope. Callers treat this as a degrade-to-full-
// result case rather than a hard failure, since it reflects an upstream
// tool's serialization bug, not a planner mistake.
var errResultNotJSON = fmt.Errorf("dep result is not JSON")

// validateDataFlow rejects data-consuming tool nodes that declare
// depends_on dependencies but wire NO param_refs. The planner almost
// never depends a compute (or edit_file) on upstream steps for pure
// sequencing — when it sets depends_on, it wants their data. Silence
// between "declared the dep" and "wired the data" is an omission bug:
// the compute runs with empty context, hallucinates from the goal text
// or training memory, and produces garbage or burns Holmes budget
// chasing symptoms of the missing wiring.
//
// Scope: compute + edit_file only. Other tools (bash, service, etc.)
// legitimately use depends_on for ordering side-effects with no data
// flow — those aren't caught by this check.
func validateDataFlow(toolName string, dependsOn []string, paramRefs map[string]ResolvedInjection) error {
	if len(dependsOn) == 0 || len(paramRefs) > 0 {
		return nil
	}
	if toolName != "compute" && toolName != "edit_file" {
		return nil
	}
	log.Printf("[dispatch:reject] %s: depends_on %v but no param_refs — data flow incomplete",
		toolName, dependsOn)
	return fmt.Errorf("tool %s declares depends_on %v but wires no param_refs — if you depend on those steps' data, wire it via param_refs; if you meant pure sequencing, compute/edit_file is the wrong tool for that",
		toolName, dependsOn)
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
