package agent

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

/*
 * A step written inside a step is reported as itself.
 *
 * It used to arrive as one fault per key — tool, params and tag each "not a
 * parameter of process_list" — which is true of each and says nothing about
 * what happened. Measured on a live deployment: 58 such faults across 7 runs,
 * every one of them three symptoms of one mistake, and every one of those runs
 * abandoned after three corrections that reproduced the same plan.
 */
func TestANestedStepIsNamedRatherThanReportedAsThreeFaults(t *testing.T) {
	reg := toolapi.NewRegistry()
	reg.Register(&fakeParamTool{name: "process_list", params: `{"type":"object",
		"properties":{"filter":{"type":"string"},"limit":{"type":"integer"}},
		"additionalProperties":false}`})

	steps := []PlanStep{{
		Tool: "process_list",
		Params: map[string]any{
			"tool":   "process_list",
			"params": map[string]any{"filter": "powershell.exe"},
			"tag":    "list_powershell",
		},
	}}

	errs := validatePlanParams(steps, reg)
	if len(errs) != 1 {
		t.Fatalf("got %d errors, want one naming the mistake:\n%s", len(errs), strings.Join(errs, "\n"))
	}
	got := errs[0]
	if !strings.Contains(got, "params holds a step") {
		t.Errorf("the error does not name the mistake: %s", got)
	}
	for _, key := range []string{"tool", "params", "tag"} {
		if !strings.Contains(got, key) {
			t.Errorf("the error does not say which keys were found (%s missing): %s", key, got)
		}
	}
}

// An ordinary step is untouched: the check must not swallow the per-parameter
// faults, which are the ones a correct-shaped plan gets.
func TestAnOrdinaryStepStillGetsItsParameterFaults(t *testing.T) {
	reg := toolapi.NewRegistry()
	reg.Register(&fakeParamTool{name: "inspect_process", params: `{"type":"object",
		"properties":{"pid":{"type":"integer"}},"required":["pid"],
		"additionalProperties":false}`})

	errs := validatePlanParams([]PlanStep{{Tool: "inspect_process", Params: map[string]any{}}}, reg)
	if len(errs) != 1 || !strings.Contains(errs[0], `"pid" is not supplied`) {
		t.Fatalf("want the missing-parameter fault, got: %v", errs)
	}
}

/*
 * A tool with one step-shaped parameter name of its own is not a nested step.
 *
 * Two matches are required for exactly this: "tag" is an ordinary word and a
 * tool may take one, and reading that as a nested step would refuse a correct
 * plan — a worse failure than the one being fixed, because there is no
 * correction that would satisfy it.
 */
func TestOneStepShapedNameIsNotANestedStep(t *testing.T) {
	reg := toolapi.NewRegistry()
	reg.Register(&fakeParamTool{name: "label_thing", params: `{"type":"object",
		"properties":{"tag":{"type":"string"}},"additionalProperties":false}`})

	errs := validatePlanParams([]PlanStep{{Tool: "label_thing",
		Params: map[string]any{"tag": "a-real-parameter"}}}, reg)
	if len(errs) != 0 {
		t.Errorf("a tool's own \"tag\" parameter was read as a nested step: %v", errs)
	}
}

/*
 * Every params example in the prompt has to parse as the JSON object it claims
 * to be.
 *
 * Four of the nine were double-escaped — `\\"` where `\"` was meant, the level
 * a Go source literal needs, reached the markdown unchanged — so the file
 * taught two escapings at once and the block labelled Examples was the wrong
 * one. Nothing fails when that happens: not a build, not a call, not a log.
 * Only a model reading it, and only the ones that cannot tell it is a mistake.
 */
func TestEveryParamsExampleInThePromptParses(t *testing.T) {
	src := promptsMarkdown(t)
	re := regexp.MustCompile(`"params"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	found := 0
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		raw := m[1]
		if strings.HasPrefix(raw, "<") {
			continue // a placeholder describing the shape, not an example of it
		}
		found++
		unquoted := strings.ReplaceAll(raw, `\"`, `"`)
		var obj map[string]any
		if err := json.Unmarshal([]byte(unquoted), &obj); err != nil {
			t.Errorf("params example does not parse: %s\n  %v", raw, err)
		}
	}
	if found < 5 {
		t.Fatalf("only %d params examples found; the pattern has drifted from the file", found)
	}
}

// The prompt must not show a step inside params, which is what the planner was
// doing and what one sentence of the prompt used to point at.
func TestThePromptNeverShowsAStepInsideParams(t *testing.T) {
	src := promptsMarkdown(t)
	re := regexp.MustCompile(`"params"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		inner := strings.ReplaceAll(m[1], `\"`, `"`)
		var obj map[string]any
		if json.Unmarshal([]byte(inner), &obj) != nil {
			continue
		}
		if len(nestedStepKeys(obj)) > 0 {
			t.Errorf("a params example contains a step's own keys: %s", m[1])
		}
	}
}

// promptsMarkdown reads the canonical prompt text. From the file rather than
// the embedded copy, because the embed lives in another package and this test
// is about what is written there.
func promptsMarkdown(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("prompt/prompts.md")
	if err != nil {
		t.Fatalf("read prompts.md: %v", err)
	}
	return string(b)
}

type fakeParamTool struct {
	name   string
	params string
}

func (f *fakeParamTool) Name() string                { return f.name }
func (f *fakeParamTool) Description() string         { return f.name }
func (f *fakeParamTool) Parameters() json.RawMessage { return json.RawMessage(f.params) }
func (f *fakeParamTool) Impact(_ map[string]any) int { return toolapi.ImpactObserve }
func (f *fakeParamTool) Execute(_ context.Context, _ map[string]any) (string, error) {
	return "", nil
}
