package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Compdeep/kaiju/internal/agent/tools"
)

/*
 * ComputeTool is the registered tool entry for compute.
 * desc: Implements both the standard Tool interface (for registry/schema
 *       visibility to the planner) and ContextualExecutor (the real
 *       execution path — compute needs graph, budget, LLM clients that
 *       plain Execute cannot carry). The dispatcher always prefers
 *       ExecuteWithContext when a tool implements it, so the plain
 *       Execute method below is a defensive stub that should never be
 *       reached in normal operation.
 */
type ComputeTool struct {
	agent *Agent
}

// Compile-time interface assertions.
var _ tools.Tool = (*ComputeTool)(nil)
var _ ContextualExecutor = (*ComputeTool)(nil)

/*
 * NewComputeTool constructs a ComputeTool bound to an Agent.
 * desc: The agent reference gives ExecuteWithContext access to llm clients,
 *       workspace, and the runCompute entry point.
 * param: a - the agent instance to bind.
 * return: a new ComputeTool.
 */
func NewComputeTool(a *Agent) *ComputeTool { return &ComputeTool{agent: a} }

func (c *ComputeTool) Name() string { return "compute" }

func (c *ComputeTool) Description() string {
	return "Compute a VALUE via a runnable script, or scaffold a whole new project. " +
		"Shallow mode: the Coder emits a script, the script runs, stdout is captured " +
		"on `.output` for downstream param_refs — use this for analytics, rankings, " +
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
		"context":    {"type": "object", "description": "Data from upstream steps (injected via param_refs)"},
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
// param_refs at real field names instead of guessing. `output` is the
// captured stdout of the executed script — the field downstream steps chain
// on when they need the computed value.
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

/*
 * Execute is a defensive stub.
 * desc: Compute requires a live graph, budget, and LLM clients that plain
 *       (ctx, params) cannot carry. The dispatcher always prefers
 *       ExecuteWithContext when the tool implements ContextualExecutor, so
 *       this path is unreachable in normal use. It exists only to satisfy
 *       the Tool interface for registry compatibility.
 */
func (c *ComputeTool) Execute(_ context.Context, _ map[string]any) (string, error) {
	return "", fmt.Errorf("compute requires graph context; invoke via dispatcher")
}

/*
 * ExecuteWithContext is the real entry point for compute.
 * desc: Delegates to the agent's runCompute method which runs the
 *       architect/coder pipeline.
 * param: ec - the execute context with graph/budget/LLM references.
 * param: params - the resolved tool parameters.
 * return: the compute result JSON and any error.
 */
func (c *ComputeTool) ExecuteWithContext(ec *ExecuteContext, params map[string]any) (string, error) {
	return c.agent.runCompute(ec, params)
}
