package llm

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Whether a schema is one the provider can actually hold the model to.
//
// asSchemaRequest sends every reasoning stage's schema with strict true, and a
// provider that enforces strict rejects a schema that does not obey its two
// rules: every object closed, and every property required. A provider that does
// NOT enforce accepts the same schema and returns a plausible reply, so the
// request says strict and nothing is checking.
//
// The two are indistinguishable from the reply, which is the whole reason for
// this file. It reads the schema before it is sent and says which stages could
// be enforced and which could only ever have been advisory.
//
// It is not a JSON Schema validator. It checks the constraints strict mode adds
// on top, and nothing else.

// StrictProblem is one place a schema breaks a strict-mode rule.
type StrictProblem struct {
	// Path names the object, in dotted form from the root: "" is the schema
	// itself, ".steps[].params" is the params object inside each step.
	Path string
	// Why is the rule that was broken, in a sentence.
	Why string
}

func (p StrictProblem) String() string {
	at := p.Path
	if at == "" {
		at = "(root)"
	}
	return at + ": " + p.Why
}

/*
 * StrictProblems reports every reason a provider that enforces strict mode
 * would refuse this schema.
 * desc: Empty means the schema can be enforced. Anything else means the request
 *       will either be rejected, or accepted by a provider that is not checking
 *       — and the caller has no way to tell which from the reply.
 *
 *       Objects are walked wherever they can appear: a property, an array's
 *       items, an open map's value schema, and each branch of anyOf, oneOf and
 *       allOf. closeNested reaches the first two, which is why a schema using
 *       either of the last two can pass through it untouched.
 * param: schema - the schema as it would be sent.
 * return: the problems, in path order. Empty when there are none.
 */
func StrictProblems(schema json.RawMessage) []StrictProblem {
	var root any
	if err := json.Unmarshal(schema, &root); err != nil {
		return []StrictProblem{{Why: "not valid JSON: " + err.Error()}}
	}
	var out []StrictProblem
	walkStrict("", root, &out)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Why < out[j].Why
	})
	return out
}

// StrictOK is StrictProblems reduced to a yes or no, for a caller that only
// needs to branch.
func StrictOK(schema json.RawMessage) bool { return len(StrictProblems(schema)) == 0 }

func walkStrict(path string, node any, out *[]StrictProblem) {
	m, ok := node.(map[string]any)
	if !ok {
		return
	}

	// An enum the model is meant to choose from, with nothing in it. A schema
	// build that produced this has lost its list somewhere upstream; the model
	// is being offered no legal value for the field.
	if e, has := m["enum"]; has {
		arr, isArr := e.([]any)
		if !isArr || len(arr) == 0 {
			*out = append(*out, StrictProblem{path, "enum is empty or null, so no value is legal"})
		}
	}

	// An object is one that says so, with or without a property list. A bare
	// {"type":"object"} declares no keys and closes nothing, which reads as
	// harmless and is the same open map as any other — every key it carries is
	// undeclared. Checking only the ones with a property list missed three
	// stages that pass a tool's parameters that way.
	props, hasProps := m["properties"].(map[string]any)
	declaredType, _ := m["type"].(string)
	if hasProps || declaredType == "object" {
		if ap, has := m["additionalProperties"]; !has {
			*out = append(*out, StrictProblem{path, "additionalProperties is absent, and strict requires it to be false"})
		} else if b, isBool := ap.(bool); !isBool || b {
			*out = append(*out, StrictProblem{path, fmt.Sprintf("additionalProperties is %v, and strict requires false", ap)})
		}
	}

	if hasProps {
		declared := requiredSet(m["required"])
		var missing []string
		for k := range props {
			if !declared[k] {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			*out = append(*out, StrictProblem{path, fmt.Sprintf(
				"strict requires every property in required, and these are not: %v "+
					"(an optional field is written as a null union, not left out)", missing)})
		}

		for k, v := range props {
			walkStrict(path+"."+k, v, out)
		}
	}

	// A map with keys nobody declared. Strict has no way to describe one: the
	// grammar needs to know what may follow, and "any name at all" does not say.
	// A stage that needs one cannot be enforced, whatever the request claims.
	if ap, isSchema := m["additionalProperties"].(map[string]any); isSchema {
		*out = append(*out, StrictProblem{path,
			"additionalProperties holds a schema, which is a map with undeclared keys — strict cannot express one"})
		walkStrict(path+".<key>", ap, out)
	}

	if items, has := m["items"].(map[string]any); has {
		walkStrict(path+"[]", items, out)
	}

	for _, branchKind := range []string{"anyOf", "oneOf", "allOf"} {
		arr, has := m[branchKind].([]any)
		if !has {
			continue
		}
		for i, branch := range arr {
			walkStrict(fmt.Sprintf("%s.%s[%d]", path, branchKind, i), branch, out)
		}
	}

	for _, defsKind := range []string{"$defs", "definitions"} {
		defs, has := m[defsKind].(map[string]any)
		if !has {
			continue
		}
		for name, def := range defs {
			walkStrict(fmt.Sprintf("%s.%s.%s", path, defsKind, name), def, out)
		}
	}
}

// requiredSet reads a required list, which arrives as []any through JSON and as
// []string when closedSchema has just written it.
func requiredSet(v any) map[string]bool {
	set := map[string]bool{}
	switch list := v.(type) {
	case []any:
		for _, k := range list {
			if s, ok := k.(string); ok {
				set[s] = true
			}
		}
	case []string:
		for _, k := range list {
			set[k] = true
		}
	}
	return set
}

/*
 * SchemaAsSent returns the schema this tool would travel as, once a request
 * carrying it alone has been rewritten.
 * desc: The same closure asSchemaRequest applies, so a caller checking a stage
 *       checks what the provider will actually receive rather than what the
 *       stage declared. Nil means the tool would not convert at all and the
 *       call stays on tool calling, where none of the strict rules apply.
 * param: tool - the stage's single declared tool.
 * return: the schema as sent, or nil when the call would not convert.
 */
func SchemaAsSent(tool ToolDef) json.RawMessage {
	return closedSchema(tool.Function.Parameters)
}
