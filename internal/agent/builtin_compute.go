package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/user/kaiju/internal/agent/tools"
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
	return "Software development, programmatic computation, and complex data processing. " +
		"Use for building applications, scaffolding projects, writing multi-file code, " +
		"calculations, analytics, or any task requiring code. Language-agnostic: " +
		"supports Python, Go, Node.js, Bash, C++, Rust, and more. " +
		"Provide the GOAL (what to build), not the code."
}

func (c *ComputeTool) Impact(params map[string]any) int {
	return tools.ImpactAffect
}

var computeParamSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"goal":     {"type": "string", "description": "What to compute — describe the desired outcome, not how to implement it"},
		"mode":     {"type": "string", "enum": ["shallow","deep"], "description": "shallow: fast single pass. deep: plans approach first then implements"},
		"query":    {"type": "string", "description": "The original user request for full context"},
		"context":  {"type": "object", "description": "Data from upstream steps (injected via param_refs)"},
		"hints":    {"type": "array", "items": {"type": "string"}, "description": "Error messages from previous failed attempts"},
		"language": {"type": "string", "description": "Preferred language (auto-detected if omitted)"}
	},
	"required": ["goal", "mode"]
}`)

func (c *ComputeTool) Parameters() json.RawMessage {
	return computeParamSchema
}

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
