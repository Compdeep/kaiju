package agent

import (
	"context"
	"encoding/json"

	"github.com/Compdeep/kaiju/agent/tools"
)

/*
 * ComputeTool is the registered tool entry for compute.
 * desc: Returns a ToolMessage like every other tool. The graph, budget and
 *       LLM clients it needs — which plain (ctx, params) cannot carry — ride
 *       on the ctx, put there by the dispatcher before it calls anything.
 */
type ComputeTool struct {
	agent *Agent
}

// Compile-time interface assertions.
var _ tools.Tool = (*ComputeTool)(nil)
var _ tools.TypedExecutor = (*ComputeTool)(nil)

/*
 * NewComputeTool constructs a ComputeTool bound to an Agent.
 * desc: The agent reference gives ExecuteTyped access to llm clients,
 *       workspace, and the runCompute entry point.
 * param: a - the agent instance to bind.
 * return: a new ComputeTool.
 */
func NewComputeTool(a *Agent) *ComputeTool { return &ComputeTool{agent: a} }

func (c *ComputeTool) Name() string { return "compute" }

func (c *ComputeTool) Description() string {
	return "Compute a VALUE via a runnable script, or scaffold a whole new project. " +
		"Shallow mode: the Coder emits a script, the script runs, stdout is captured " +
		"on `.output` so downstream steps can read it via ${step.N.output} — use this for analytics, rankings, " +
		"calculations, derived data. Deep mode: architect plans then multiple coders " +
		"build — use this ONLY for new-codebase scaffolding. " +
		"DO NOT use compute to edit a specific known file — use `edit_file` for that. " +
		"Provide the GOAL, not the code."
}

func (c *ComputeTool) Impact(params map[string]any) int {
	return tools.ImpactAffect
}

var computeParamSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"goal":       {"type": "string", "description": "What to compute — describe the desired outcome, not how to implement it"},
		"mode":       {"type": "string", "enum": ["shallow","deep"], "description": "shallow: fast single pass. deep: plans approach first then implements"},
		"query":      {"type": "string", "description": "The original user request for full context"},
		"context":    {"type": "object", "description": "Data from upstream steps (wire via ${step.N.field} placeholders inside this object)"},
		"hints":      {"type": "array", "items": {"type": "string"}, "description": "Error messages from previous failed attempts"},
		"language":   {"type": "string", "description": "Preferred language (auto-detected if omitted)"},
		"task_files": {"type": "array", "items": {"type": "string"}, "description": "DEPRECATED on compute — use the edit_file tool instead for known-path file edits. Only the architect's internal tasks in deep mode set this meaningfully."}
	},
	"required": ["goal", "mode"]
}`)

func (c *ComputeTool) Parameters() json.RawMessage {
	return computeParamSchema
}

// computeOutputSchema documents what compute returns so the planner can wire
// ${step.N.field} placeholders at real field names instead of guessing.
// `output` is the captured stdout of the executed script — the field
// downstream steps reference when they need the computed value.
var computeOutputSchema = json.RawMessage(`{
	"type": "object",
	"description": "Structured compute result; 'output' holds the script's captured stdout, other fields describe the emitted code.",
	"properties": {
		"output":        {"type": "string", "description": "Captured stdout of the executed script"},
		"code_path":     {"type": "string", "description": "Absolute path of the generated code file"},
		"execute":       {"type": "string", "description": "Shell command that runs the generated code"},
		"files_created": {"type": "array", "items": {"type": "string"}, "description": "Files produced"},
		"language":      {"type": "string", "description": "Language of the emitted code"},
		"type":          {"type": "string"},
		"validation":    {"type": "string", "description": "Coder-declared validation command"}
	}
}`)

func (c *ComputeTool) OutputSchema() json.RawMessage { return computeOutputSchema }

// Execute satisfies the Tool interface for callers outside the DAG. Outside a
// run there is no graph, budget or model, so it reports that rather than
// pretending to compute anything.
func (c *ComputeTool) Execute(ctx context.Context, params map[string]any) (string, error) {
	return tools.StringResult(c.ExecuteTyped(ctx, params))
}

/*
 * ExecuteTyped is the real entry point for compute.
 * desc: Runs the architect/coder pipeline and reports what came of it. The run
 *       state compute needs — graph, budget, LLM clients — rides on the ctx,
 *       which the dispatcher puts there before it chooses how to call a tool.
 *       Without it nothing can run, and that is a failure rather than an empty
 *       result: no code was written because none was attempted.
 * param: ctx - carries the ExecuteContext.
 * param: params - the resolved tool parameters.
 * return: the envelope carrying compute's plan or result JSON as its payload.
 */
func (c *ComputeTool) ExecuteTyped(ctx context.Context, params map[string]any) (tools.ToolMessage, error) {
	ec := ExecContextFrom(ctx)
	if ec == nil {
		return tools.ToolFail("compute", "compute was called without the run state it needs — no graph, budget or model", nil), nil
	}
	raw, err := c.agent.runCompute(ec, params)
	if err != nil {
		return tools.ToolMessage{}, err
	}
	return computeMessage("compute", raw), nil
}
