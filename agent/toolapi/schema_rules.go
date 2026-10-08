package toolapi

import (
	"encoding/json"
	"fmt"
)

// What a tool's declared parameters have to be before anything will read them.
//
// Three things read a tool's Parameters(): the dispatcher checks every call
// against it, the planner's schema carries it per tool, and the settings pages
// render it. A schema that is wrong about its own tool is not a cosmetic fault
// in any of them — the dispatcher reports a violation on every correct call,
// and the planner's document is built around a shape the tool does not have.
//
// So it is checked once, at registration, and a tool that fails does not
// register. The alternative is a daemon that starts and then misbehaves on one
// tool for as long as it runs, which is the kind of fault found in production
// rather than in a test.

/*
 * ValidateParameters reports why a tool's declared parameters cannot be used.
 * desc: Four rules, each one a fault that has a consequence rather than an
 *       untidiness.
 *
 *       An empty declaration is allowed and means the tool promises nothing;
 *       the dispatcher already reads it that way.
 *
 *       Deliberately NOT a strict-mode check. Whether a schema can be enforced
 *       by a provider is a question about the provider and is asked elsewhere,
 *       per call; this asks only whether the document describes the tool it
 *       belongs to.
 * param: name - the tool's name, for the message.
 * param: raw - whatever Parameters() returned.
 * return: nil when the schema describes its tool, else what is wrong with it.
 */
func ValidateParameters(name string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil // declared nothing, promised nothing
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("tool %q: parameters are not a JSON object: %w", name, err)
	}
	if len(doc) == 0 {
		return nil // {} says the same as no document at all
	}
	var declaredType string
	if t, ok := doc["type"]; ok {
		_ = json.Unmarshal(t, &declaredType)
	}
	if declaredType != "object" {
		return fmt.Errorf(`tool %q: parameters must declare "type": "object", not %q — `+
			`every reader of this treats it as a map of named arguments`, name, declaredType)
	}
	rawProps, present := doc["properties"]
	if !present {
		return fmt.Errorf(`tool %q: parameters declare no "properties" key — `+
			`write {} for a tool that takes no arguments, so the absence is a statement `+
			`rather than an omission`, name)
	}
	var props map[string]json.RawMessage
	if err := json.Unmarshal(rawProps, &props); err != nil {
		return fmt.Errorf("tool %q: properties are not an object: %w", name, err)
	}
	if rawReq, ok := doc["required"]; ok {
		var req []string
		if err := json.Unmarshal(rawReq, &req); err != nil {
			return fmt.Errorf("tool %q: required is not a list of names: %w", name, err)
		}
		for _, k := range req {
			if _, declared := props[k]; !declared {
				return fmt.Errorf("tool %q: required names %q, which is not one of its "+
					"properties — a caller omitting it would be refused for a parameter "+
					"the tool cannot read", name, k)
			}
		}
	}
	return nil
}
