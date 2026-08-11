package agent

import (
	"encoding/json"
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
	         "depends_on":[0],"tag":"lookup","target":"self","gap":"none"}`
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
	case s.Gap != "none":
		t.Errorf("Gap = %q", s.Gap)
	case len(s.DependsOn) != 1 || s.DependsOn[0] != 0:
		t.Errorf("DependsOn = %v", s.DependsOn)
	case s.Params["ip"] != "8.8.8.8":
		t.Errorf("Params = %v", s.Params)
	}
}
