package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// outputTool is a test tool that declares an OutputSchema (implements Outputter),
// so validatePlanEdges can read its output shape. fakeTool can't — it has no
// output schema — so it can't exercise the wrong-producer / field-existence path.
type outputTool struct {
	name   string
	params json.RawMessage
	output json.RawMessage
}

func (o *outputTool) Name() string                                            { return o.name }
func (o *outputTool) Description() string                                     { return "" }
func (o *outputTool) Parameters() json.RawMessage                             { return o.params }
func (o *outputTool) Impact(map[string]any) int                               { return 0 }
func (o *outputTool) Execute(context.Context, map[string]any) (string, error) { return "", nil }
func (o *outputTool) OutputSchema() json.RawMessage                           { return o.output }

var (
	_ toolapi.Tool      = (*outputTool)(nil)
	_ toolapi.Outputter = (*outputTool)(nil)
)

// planValidateReg builds a registry with web_search and web_fetch whose output
// schemas are wrapped in the SAME envelope production uses (EnvelopeSchema), so
// web_search's results[] sits under data, exactly like the real tool.
func planValidateReg() *toolapi.Registry {
	reg := toolapi.NewRegistry()
	reg.Replace(&outputTool{
		name:   "web_search",
		params: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`),
		output: toolapi.EnvelopeSchema(`{"type":"object","properties":{"query":{"type":"string"},"results":{"type":"array","items":{"type":"object","properties":{"url":{"type":"string"},"title":{"type":"string"}}}}}}`),
	}, "builtin")
	reg.Replace(&outputTool{
		name:   "web_fetch",
		params: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"],"additionalProperties":false}`),
		output: toolapi.EnvelopeSchema(`{"type":"object","properties":{"content":{"type":"string"}}}`),
	}, "builtin")
	return reg
}

// TestValidatePlanEdges_SearchFetchEnvelope is the regression for the false
// positive: a valid search→fetch plan (web_fetch reads ${step.0.results.0.url}
// from web_search) must be CLEAN. web_search's results[] lives under the
// envelope's data, and the reference resolves against the unwrapped payload at
// runtime — so the plan-time check must look inside data too, not only the top
// level. It must still flag a genuinely wrong producer (results[] off web_fetch).
func TestValidatePlanEdges_SearchFetchEnvelope(t *testing.T) {
	reg := planValidateReg()

	valid := []PlanStep{
		{Tool: "web_search", Params: map[string]any{"query": "last winter olympics"}},
		{Tool: "web_fetch", Params: map[string]any{"url": "${step.0.results.0.url}"}},
	}
	if errs := validatePlanEdges(valid, reg); len(errs) != 0 {
		t.Fatalf("valid search→fetch was flagged as broken: %v", errs)
	}

	// Guard against the fix just disabling the check: reading results[] off a
	// web_fetch (which produces content, not results) must still be flagged.
	broken := []PlanStep{
		{Tool: "web_fetch", Params: map[string]any{"url": "http://x"}},
		{Tool: "web_fetch", Params: map[string]any{"url": "${step.0.results.0.url}"}},
	}
	if errs := validatePlanEdges(broken, reg); len(errs) == 0 {
		t.Fatalf("reading results[] off web_fetch should be flagged, but was not")
	}
}
