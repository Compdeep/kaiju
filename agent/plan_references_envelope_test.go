package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// envTool declares its output the way every core tool does: a uniform envelope
// whose `content` is the rendered text, with the tool's own fields under `data`.
type envTool struct{ name, payload string }

func (e *envTool) Name() string        { return e.name }
func (e *envTool) Description() string { return "a tool" }
func (e *envTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"goal":{"type":"string"}}}`)
}
func (*envTool) Impact(map[string]any) int { return 0 }
func (*envTool) RequiresTarget() bool      { return false }
func (*envTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}
func (e *envTool) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(e.payload)
}

func envRegistry(t *testing.T) *toolapi.Registry {
	t.Helper()
	reg := toolapi.NewRegistry()
	// file_read's real shape: the file's text is the envelope's content, and
	// data carries only bookkeeping.
	if err := reg.Register(&envTool{name: "file_read", payload: `{"type":"object","properties":{
		"path":{"type":"string"},"lines_shown":{"type":"integer"},"lines_total":{"type":"integer"}}}`}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(&envTool{name: "compute", payload: `{"type":"object","properties":{"output":{"type":"string"}}}`}); err != nil {
		t.Fatal(err)
	}
	return reg
}

// The hand-off the planner's own prompt teaches: read a file, then compute over
// its text. `content` is the envelope's, not the payload's, and the validator
// used to reject it — so a correct plan was thrown out, and the planner spent
// its three corrections being told to fix something that was right.
func TestValidatePlanReferences_AcceptsEnvelopeContent(t *testing.T) {
	steps := []PlanStep{
		{Tool: "file_read", Params: map[string]any{"path": "ttm.csv"}},
		{Tool: "compute", Params: map[string]any{"goal": "rank rows", "context.csv": "${step.0.content}"}, DependsOn: []int{0}},
	}
	if errs := validatePlanReferences(steps, envRegistry(t)); len(errs) != 0 {
		t.Errorf("a correct file_read → compute plan was rejected: %v", errs)
	}
}

// Every field the envelope declares, not just content.
func TestValidatePlanReferences_AcceptsEveryEnvelopeField(t *testing.T) {
	reg := envRegistry(t)
	for _, field := range []string{"content", "status", "type", "detail"} {
		steps := []PlanStep{
			{Tool: "file_read", Params: map[string]any{"path": "x"}},
			{Tool: "compute", Params: map[string]any{"goal": "${step.0." + field + "}"}, DependsOn: []int{0}},
		}
		if errs := validatePlanReferences(steps, reg); len(errs) != 0 {
			t.Errorf("envelope field %q was rejected: %v", field, errs)
		}
	}
}

// The tool's own fields still validate, and a field no tool produces is still
// caught — widening the check must not switch it off.
func TestValidatePlanReferences_StillCatchesRealMistakes(t *testing.T) {
	reg := envRegistry(t)

	ok := []PlanStep{
		{Tool: "file_read", Params: map[string]any{"path": "x"}},
		{Tool: "compute", Params: map[string]any{"goal": "${step.0.lines_total}"}, DependsOn: []int{0}},
	}
	if errs := validatePlanReferences(ok, reg); len(errs) != 0 {
		t.Errorf("a payload field was rejected: %v", errs)
	}

	bad := []PlanStep{
		{Tool: "file_read", Params: map[string]any{"path": "x"}},
		{Tool: "compute", Params: map[string]any{"goal": "${step.0.results}"}, DependsOn: []int{0}},
	}
	if errs := validatePlanReferences(bad, reg); len(errs) == 0 {
		t.Error("a field the producing tool does not have was accepted")
	}

	self := []PlanStep{
		{Tool: "compute", Params: map[string]any{"goal": "${step.0.content}"}},
	}
	if errs := validatePlanReferences(self, reg); len(errs) == 0 {
		t.Error("a step reading its own output was accepted")
	}
}
