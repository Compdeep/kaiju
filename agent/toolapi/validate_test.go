package toolapi

import (
	"encoding/json"
	"strings"
	"testing"
)

const bashish = `{
  "type": "object",
  "properties": {
    "command":     {"type": "string"},
    "timeout_sec": {"type": "integer", "minimum": 1, "maximum": 600},
    "quiet":       {"type": "boolean"},
    "mode":        {"type": "string", "enum": ["run", "dry"]},
    "paths":       {"type": "array", "items": {"type": "string"}},
    "options":     {"type": "object", "properties": {"retries": {"type": "integer"}}}
  },
  "required": ["command"]
}`

func check(t *testing.T, raw string) []Violation {
	t.Helper()
	var params map[string]any
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		t.Fatalf("fixture is not JSON: %v", err)
	}
	return ValidateParams(json.RawMessage(bashish), params)
}

func only(t *testing.T, vs []Violation) Violation {
	t.Helper()
	if len(vs) != 1 {
		t.Fatalf("want exactly one violation, got %d: %v", len(vs), vs)
	}
	return vs[0]
}

// A call that matches what the tool asked for says nothing.
func TestAConformingCallIsSilent(t *testing.T) {
	if vs := check(t, `{"command":"ls -la","timeout_sec":30,"quiet":true,"mode":"run",
		"paths":["/tmp","/var"],"options":{"retries":2}}`); len(vs) > 0 {
		t.Errorf("a conforming call was reported: %v", vs)
	}
}

/*
 * The fault this exists for. params["command"].(string) on a number gives "",
 * not an error, and a tool that does not check for empty proceeds with a zero
 * value — which in this codebase frequently means BROADER, not narrower.
 */
func TestAWrongTypeIsReportedRatherThanBecomingAZeroValue(t *testing.T) {
	v := only(t, check(t, `{"command": 42}`))
	if v.Path != "command" || v.Declared != "string" || v.Got != "integer" {
		t.Errorf("violation = %v, want command declared string got integer", v)
	}
}

// A missing required field is an omission, not a type error, and reads as one.
func TestAMissingRequiredFieldIsNamedAsAbsent(t *testing.T) {
	v := only(t, check(t, `{"timeout_sec": 5}`))
	if v.Path != "command" || v.Declared != "required" || v.Got != "absent" {
		t.Errorf("violation = %v, want command required/absent", v)
	}
}

// An optional field left out is not a fault, and an explicit null is the model
// saying "no value", which is the same thing.
func TestAnAbsentOrNullOptionalIsNotAFault(t *testing.T) {
	if vs := check(t, `{"command":"ls"}`); len(vs) > 0 {
		t.Errorf("an absent optional was reported: %v", vs)
	}
	if vs := check(t, `{"command":"ls","timeout_sec":null}`); len(vs) > 0 {
		t.Errorf("a null optional was reported: %v", vs)
	}
}

/*
 * JSON has one number type, so "integer" cannot be a Go type check: it is a
 * float64 with nothing after the point. ParamInt does int(f) with no check, so
 * without this a fractional value is truncated silently and a huge one becomes
 * a number that is not the one that was sent.
 */
func TestIntegerMeansAWholeNumberInRange(t *testing.T) {
	if vs := check(t, `{"command":"ls","timeout_sec":30}`); len(vs) > 0 {
		t.Errorf("a whole number was reported: %v", vs)
	}
	if v := only(t, check(t, `{"command":"ls","timeout_sec":2.5}`)); v.Got != "number" {
		t.Errorf("violation = %v, want a fractional value reported as number", v)
	}
	if vs := check(t, `{"command":"ls","timeout_sec":1e300}`); len(vs) == 0 {
		t.Error("1e300 passed as an integer — ParamInt would turn it into something else entirely")
	}
}

func TestRangeAndEnumAreHeldTo(t *testing.T) {
	if v := only(t, check(t, `{"command":"ls","timeout_sec":0}`)); !strings.Contains(v.Declared, "at least") {
		t.Errorf("violation = %v, want a minimum", v)
	}
	if v := only(t, check(t, `{"command":"ls","timeout_sec":9000}`)); !strings.Contains(v.Declared, "at most") {
		t.Errorf("violation = %v, want a maximum", v)
	}
	if v := only(t, check(t, `{"command":"ls","mode":"delete"}`)); !strings.Contains(v.Declared, "one of") {
		t.Errorf("violation = %v, want an enum", v)
	}
}

// A wrong type says one thing about one fault. Reporting the enum miss beside it
// describes the same fault twice in two vocabularies.
func TestAWrongTypeIsSaidOnce(t *testing.T) {
	if v := only(t, check(t, `{"command":"ls","mode":7}`)); v.Declared != "string" {
		t.Errorf("violation = %v, want only the type fault", v)
	}
}

func TestArrayElementsAndNestedObjectsAreReached(t *testing.T) {
	if v := only(t, check(t, `{"command":"ls","paths":["/tmp",9]}`)); v.Path != "paths[1]" {
		t.Errorf("violation = %v, want paths[1]", v)
	}
	if v := only(t, check(t, `{"command":"ls","options":{"retries":"three"}}`)); v.Path != "options.retries" {
		t.Errorf("violation = %v, want options.retries", v)
	}
}

/*
 * An unknown property is not a fault. bash accepts cmd and script, neither of
 * which is in its schema, because models send them and the tool chose to cope —
 * rejecting extras would break a tolerance that was added on purpose.
 */
func TestAnUnknownPropertyIsNotAFault(t *testing.T) {
	if vs := check(t, `{"command":"ls","cmd":"ls","script":"ls","whatever":{"deep":1}}`); len(vs) > 0 {
		t.Errorf("an unknown property was reported: %v", vs)
	}
}

/*
 * A tool that declared nothing has promised nothing. Reporting that as a fault
 * would put every schemaless tool permanently in violation, and an unreadable
 * schema is a fault in the tool rather than in the call that arrived.
 */
func TestNothingDeclaredMeansNothingToHoldTo(t *testing.T) {
	for _, schema := range []string{``, `{}`, `{"type":"object"}`, `not json at all`} {
		if vs := ValidateParams(json.RawMessage(schema), map[string]any{"anything": 1}); len(vs) > 0 {
			t.Errorf("schema %q reported %v", schema, vs)
		}
	}
	if vs := ValidateParams(json.RawMessage(bashish), nil); len(vs) != 1 {
		t.Errorf("a nil map should still miss its required field, got %v", vs)
	}
}
