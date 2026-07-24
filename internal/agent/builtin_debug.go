package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Compdeep/kaiju/internal/agent/tools"
)

// debugToolName is the registered name of the debug super-tool. It is pruned
// from Holmes's and the microplanner's tool lists (a debug must never spawn a
// debug) — see rca.go and the microplanner graft in scheduler.go.
const debugToolName = "debug"

/*
 * DebugTool is the registered tool entry for the debug super-tool.
 * desc: `debug` is the REPAIR super-tool — the executive-planned door to the
 *       failure-handling pipeline (Holmes root-cause analysis → clean-room
 *       microplanner fix → validators). It mirrors `compute`: a thin tool
 *       interface over a DAG sub-structure the scheduler grafts.
 *
 *       The tool itself does almost nothing — ExecuteWithContext echoes the
 *       problem statement into a {type:"debug"} envelope. The real work is
 *       grafted by the scheduler when the debug node completes: it snapshots
 *       the currently-failed nodes and spawns the first Holmes iteration
 *       parented to the debug node. From there the existing NodeHolmes →
 *       NodeMicroPlanner → validator machinery drives the fix, fully visible
 *       in the DAG trace.
 *
 *       Repair thus flows through the SAME door as expand: reflect.replan →
 *       executive plans a `debug` step → this graft. There is no `investigate`
 *       reflection decision anymore.
 */
type DebugTool struct {
	agent *Agent
}

// Compile-time interface assertions. debug implements ContextualExecutor so its
// envelope is never truncated (the scheduler must parse the {type:"debug"}
// marker to trigger the graft) — same reason compute does.
var _ tools.Tool = (*DebugTool)(nil)
var _ ContextualExecutor = (*DebugTool)(nil)

// NewDebugTool constructs a DebugTool bound to an Agent.
func NewDebugTool(a *Agent) *DebugTool { return &DebugTool{agent: a} }

func (d *DebugTool) Name() string { return debugToolName }

func (d *DebugTool) Description() string {
	return "Diagnose and FIX a failed step. Spawns Holmes (a read-only root-cause investigator), " +
		"then a clean-room debugger plans and applies the fix, then validators confirm it. " +
		"Use this ONLY when a prior step FAILED and the failure is inside the agent's control — " +
		"pass the exact error text, file paths, and module names in `problem`. " +
		"Do NOT use it for transient errors (timeouts, HTTP 5xx, rate limits, empty results), " +
		"for out-of-scope or unfixable-environment failures (sudo/root, OS package managers, a missing " +
		"language runtime), or when nothing actually failed. One debug step per failure — plan it as a " +
		"leaf; the next re-plan handles follow-on work once the fix lands."
}

func (d *DebugTool) Impact(params map[string]any) int {
	// Write-capable: the microplanner fix edits files. IGX gates it like
	// compute so lanes below the required clearance can't invoke it.
	return tools.ImpactAffect
}

var debugParamSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"problem": {"type": "string", "description": "The failure to diagnose and fix: exact error messages, file paths, module names, and what was being attempted. This is Holmes's investigation brief."}
	},
	"required": ["problem"]
}`)

func (d *DebugTool) Parameters() json.RawMessage { return debugParamSchema }

var debugOutputSchema = json.RawMessage(`{
	"type": "object",
	"description": "Debug trigger envelope. The investigation + fix are grafted as child nodes; their results resolve on those nodes, not here.",
	"properties": {
		"type":    {"type": "string"},
		"problem": {"type": "string", "description": "The investigation brief echoed back"}
	}
}`)

func (d *DebugTool) OutputSchema() json.RawMessage { return debugOutputSchema }

/*
 * Execute is a defensive stub — debug is always dispatched via
 * ExecuteWithContext (it implements ContextualExecutor). Present only to
 * satisfy the Tool interface for registry compatibility.
 */
func (d *DebugTool) Execute(_ context.Context, _ map[string]any) (string, error) {
	return "", fmt.Errorf("debug requires graph context; invoke via dispatcher")
}

/*
 * ExecuteWithContext echoes the problem into a {type:"debug"} envelope. The
 * scheduler's tool-completion handler detects this marker and grafts the Holmes
 * investigation parented to this node.
 * param: ec - the execute context (unused beyond satisfying the interface).
 * param: params - resolved tool params; `problem` is the investigation brief.
 * return: the debug envelope JSON.
 */
func (d *DebugTool) ExecuteWithContext(_ *ExecuteContext, params map[string]any) (string, error) {
	problem, _ := params["problem"].(string)
	env := map[string]string{"type": "debug", "problem": problem}
	b, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("debug: marshal envelope: %w", err)
	}
	return string(b), nil
}
