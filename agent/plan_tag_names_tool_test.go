package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A step named after one tool that calls another.
//
// qwen3.6 on OpenRouter writes a step's keys alphabetically, so params comes
// before tool, and the parameters it writes decide which tool the schema
// accepts. Run 72faf339 planned {"params":{"query":""},"tag":"process_list",
// "tool":"message_search"}; the correction said only that message_search
// requires query, and three corrections returned the same step.
func TestACorrectionNamesTheToolTheStepIsNamedAfter(t *testing.T) {
	reg := toolapi.NewRegistry()
	reg.Replace(&refTool{name: "message_search", schema: `{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"}},"required":["query"]}`}, "builtin")
	reg.Replace(&refTool{name: "process_list", schema: `{"type":"object","properties":{"filter":{"type":"string"},"limit":{"type":"integer"}}}`}, "builtin")
	errs := validatePlanParams([]PlanStep{{Tool: "message_search", Tag: "process_list", Params: map[string]any{"query": ""}}}, reg)
	all := strings.Join(errs, "\n")
	if !strings.Contains(all, `named "process_list" but calls message_search`) || !strings.Contains(all, `change "tool" to "process_list"`) {
		t.Errorf("the correction does not name the tool the step is named after:\n%s", all)
	}
}

// A tag that is not a tool's name adds nothing.
func TestACorrectionForAnOrdinaryTagIsUnchanged(t *testing.T) {
	reg := toolapi.NewRegistry()
	reg.Replace(&refTool{name: "message_search", schema: `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`}, "builtin")
	errs := validatePlanParams([]PlanStep{{Tool: "message_search", Tag: "find_notes", Params: map[string]any{}}}, reg)
	if all := strings.Join(errs, "\n"); strings.Contains(all, "is named") {
		t.Errorf("a tag that names no tool got the hint:\n%s", all)
	}
}

func mustRegister(t *testing.T, reg *toolapi.Registry, tool toolapi.Tool) {
	t.Helper()
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
}
