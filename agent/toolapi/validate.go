package toolapi

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

/*
 * Checking a call against the shape its tool asked for.
 *
 * A tool declares its parameters as a JSON Schema, that schema is sent to the
 * model to shape the request, and until now nothing looked at it again when the
 * reply came back. What arrives is map[string]any decoded from a string the
 * model wrote, and the only thing standing between it and the tool was the
 * tool's own type assertions.
 *
 * Those assertions are safe in Go and dangerous in logic. params["host"] on a
 * number gives "", not an error, and an empty value frequently means BROADER
 * rather than narrower — a rule that names no host is one that reaches every
 * host. A malformed parameter does not fail the call, it widens it.
 *
 * This checks only what the tool declared. Semantic rules — one of these two
 * fields but not both, this path must exist, this duration is absurd — stay with
 * the tool, which is the only thing that knows them. The split is deliberate:
 * what is declared is checked in one place for every tool, and what is meant is
 * checked by the tool that means it.
 */

// Violation is one parameter that does not match what the tool declared.
type Violation struct {
	Path     string // "command", or "options.retries" inside a nested object
	Declared string // what the schema asked for
	Got      string // what arrived
}

func (v Violation) String() string {
	return fmt.Sprintf("%s: declared %s, got %s", v.Path, v.Declared, v.Got)
}

/*
 * ValidateParams reports every way a call departs from its tool's schema.
 *
 * Unknown properties are NOT a violation. bash accepts cmd and script, neither
 * of which is in its schema, because models send them and the tool chose to
 * cope; rejecting extras would break a tolerance that was added on purpose.
 *
 * A schema that does not parse, or that declares no properties, yields nothing.
 * The check exists to hold tools to what they said, and a tool that said nothing
 * has promised nothing — reporting that as a fault would put every schemaless
 * tool permanently in violation.
 */
func ValidateParams(schema json.RawMessage, params map[string]any) []Violation {
	if len(schema) == 0 {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(schema, &doc); err != nil {
		return nil
	}
	return validateObject(doc, params, "")
}

// validateObject checks one object schema against one decoded map.
func validateObject(schema map[string]any, params map[string]any, prefix string) []Violation {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	var out []Violation

	// Required first: a missing field is a different fault from a wrong one, and
	// saying "declared string, got nothing" for an absent key reads as a type
	// error rather than an omission.
	for _, name := range stringsOf(schema["required"]) {
		if _, present := params[name]; !present {
			out = append(out, Violation{Path: prefix + name, Declared: "required", Got: "absent"})
		}
	}

	for name, raw := range props {
		spec, _ := raw.(map[string]any)
		if spec == nil {
			continue
		}
		value, present := params[name]
		// An absent optional field is not a fault, and an absent required one
		// has already been reported above. A present null is the model saying
		// "no value", which is the same thing.
		if !present || value == nil {
			continue
		}
		out = append(out, validateValue(spec, value, prefix+name)...)
	}
	return out
}

/*
 * validateValue checks one value against one property schema.
 *
 * Type first and alone: once the type is wrong there is nothing useful to say
 * about range or membership, and reporting "declared integer, got string" beside
 * "not one of [a b c]" describes one fault twice.
 */
func validateValue(spec map[string]any, value any, path string) []Violation {
	declared, _ := spec["type"].(string)
	if declared != "" && !typeMatches(declared, value) {
		return []Violation{{Path: path, Declared: declared, Got: typeName(value)}}
	}

	var out []Violation
	if members := stringsOf(spec["enum"]); len(members) > 0 {
		if s, ok := value.(string); ok && !contains(members, s) {
			out = append(out, Violation{Path: path,
				Declared: "one of [" + strings.Join(members, " ") + "]", Got: quote(s)})
		}
	}
	if n, ok := value.(float64); ok {
		if min, has := numberOf(spec["minimum"]); has && n < min {
			out = append(out, Violation{Path: path, Declared: fmt.Sprintf("at least %g", min), Got: fmt.Sprintf("%g", n)})
		}
		if max, has := numberOf(spec["maximum"]); has && n > max {
			out = append(out, Violation{Path: path, Declared: fmt.Sprintf("at most %g", max), Got: fmt.Sprintf("%g", n)})
		}
	}
	if items, _ := spec["items"].(map[string]any); items != nil {
		if list, ok := value.([]any); ok {
			for i, item := range list {
				if item == nil {
					continue
				}
				out = append(out, validateValue(items, item, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	}
	if nested, _ := value.(map[string]any); nested != nil && declared == "object" {
		out = append(out, validateObject(spec, nested, path+".")...)
	}
	return out
}

/*
 * typeMatches reports whether a decoded JSON value is the declared type.
 *
 * Every JSON number decodes to float64, so "integer" cannot be a type check in
 * Go — it is float64 with nothing after the point, and inside the range an int
 * can hold. Without that last part ParamInt turns 1e300 into a number that is
 * not 1e300 and says nothing, which is the quiet half of this whole problem.
 */
func typeMatches(declared string, value any) bool {
	switch declared {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		n, ok := value.(float64)
		if !ok {
			return false
		}
		return n == math.Trunc(n) && n >= math.MinInt64 && n <= math.MaxInt64
	case "array":
		if _, ok := value.([]any); ok {
			return true
		}
		_, ok := value.([]string) // a Go caller may pass the native shape
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "null":
		return value == nil
	}
	return true // a type this does not know is not a type it can refuse
}

// typeName names what actually arrived, in the schema's vocabulary, so a
// violation reads in one language rather than two.
func typeName(v any) string {
	switch n := v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		if n == math.Trunc(n) {
			return "integer"
		}
		return "number"
	case []any, []string:
		return "array"
	case map[string]any:
		return "object"
	case nil:
		return "null"
	}
	return fmt.Sprintf("%T", v)
}

func stringsOf(v any) []string {
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func numberOf(v any) (float64, bool) {
	n, ok := v.(float64)
	return n, ok
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
