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

// validateParamRef checks that the named field is actually present in
// the dep's resolved Result. Called from resolveInjections between
// dep-lookup and value-substitution. Replaces the prior silent fallback
// that used the full dep Result whenever the field was absent — the
// bug shape that let compute's absent .output land as file_write's
// content and clobber target files.
//
// Empty ref.Field means "use the whole result as-is" and is a legitimate
// planner choice (e.g. chaining raw file_read output). Not an error.
func validateParamRef(paramName string, ref ResolvedInjection, depResult string) error {
	if ref.Field == "" {
		return nil
	}
	if _, err := extractJSONField(depResult, ref.Field); err != nil {
		log.Printf("[dispatch:reject] param_ref %q: field %q absent in dep %s (%v)",
			paramName, ref.Field, ref.NodeID, err)
		return fmt.Errorf("param_ref %q: field %q absent in dep %s (%w)",
			paramName, ref.Field, ref.NodeID, err)
	}
	return nil
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
