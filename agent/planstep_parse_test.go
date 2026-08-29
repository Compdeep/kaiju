package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// A plan step keeps the machine it names.
//
// PlanStep declares Target and its own UnmarshalJSON decoded into a private
// struct that had no such field, so a step naming a machine arrived with an
// empty one and ran wherever the agent happened to be. Nothing failed: an empty
// target is exactly what a step that names no machine looks like.
func TestAPlanStepKeepsItsTarget(t *testing.T) {
	var s PlanStep
	if err := json.Unmarshal([]byte(`{"tool":"lookup_ip","tag":"lookup","target":"other-machine"}`), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.Target != "other-machine" {
		t.Errorf("Target = %q, want other-machine — a step that named a machine "+
			"would run on whichever one the agent is", s.Target)
	}
}

// Every declared field survives the round trip.
func TestAPlanStepKeepsEveryDeclaredField(t *testing.T) {
	var s PlanStep
	raw := `{"type":"tool","tool":"lookup_ip","params":{"ip":"8.8.8.8"},
	         "depends_on":[0],"tag":"lookup","target":"self"}`
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	switch {
	case s.Type != "tool":
		t.Errorf("Type = %q", s.Type)
	case s.Tool != "lookup_ip":
		t.Errorf("Tool = %q", s.Tool)
	case s.Tag != "lookup":
		t.Errorf("Tag = %q", s.Tag)
	case s.Target != "self":
		t.Errorf("Target = %q", s.Target)
	case len(s.DependsOn) != 1 || s.DependsOn[0] != 0:
		t.Errorf("DependsOn = %v", s.DependsOn)
	case s.Params["ip"] != "8.8.8.8":
		t.Errorf("Params = %v", s.Params)
	}
}

// A field on a step survives being decoded, whatever it is.
//
// The decoder used to declare a second struct listing every field by hand and
// copy them across one at a time. A field added to PlanStep and not to that
// copy was dropped with no error, which is what happened to Target: a plan
// named the machine to run on and the step arrived without one.
//
// It decodes into PlanStep itself now, so there is no second list to fall
// behind. This walks the struct and checks every field arrives, so a field
// added later is covered without anyone remembering to add a case.
func TestEveryFieldOfAStepSurvivesDecoding(t *testing.T) {
	raw := `{
		"type": "compute",
		"tool": "bash",
		"params": {"cmd": "ls"},
		"depends_on": [2],
		"tag": "list the directory",
		"target": "machine-b"
	}`

	var step PlanStep
	if err := json.Unmarshal([]byte(raw), &step); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	v := reflect.ValueOf(step)
	ty := v.Type()
	for i := 0; i < ty.NumField(); i++ {
		name, _, _ := strings.Cut(ty.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		if v.Field(i).IsZero() {
			t.Errorf("%s was set in the JSON and is empty on the step — the decoder dropped it. "+
				"If the field is new, nothing had to be updated for it to work, so this is a real drop",
				ty.Field(i).Name)
		}
	}
}

// The warning about fields the model invented reads the struct too, so a new
// field does not start producing a spurious "unknown field" line.
func TestAKnownFieldIsNotReportedAsInvented(t *testing.T) {
	ty := reflect.TypeOf(PlanStep{})
	for i := 0; i < ty.NumField(); i++ {
		name, _, _ := strings.Cut(ty.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		if !planStepFields[name] {
			t.Errorf("%q is a field of a step and is not in planStepFields, so a step carrying "+
				"it is logged as having invented it", name)
		}
	}
	if !planStepFields["param_refs"] {
		t.Error("param_refs is accepted by the decoder and would be reported as invented")
	}
}

// The legacy shape still works, and it is not a field of PlanStep — so the
// change above must not have taken it with the hand-written copy.
func TestALegacyParamRefIsStillLifted(t *testing.T) {
	var step PlanStep
	err := json.Unmarshal([]byte(`{"tool":"bash","param_refs":{"cmd":"${step.0.path}"}}`), &step)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if step.Params["cmd"] != "${step.0.path}" {
		t.Errorf("params[cmd] = %v, want the lifted template", step.Params["cmd"])
	}
}
