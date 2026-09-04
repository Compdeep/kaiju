package agent

import (
	"context"
	"encoding/json"

	"github.com/Compdeep/kaiju/agent/toolapi"
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
 *       The tool itself does almost nothing — ExecuteTyped echoes the
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

// Compile-time interface assertions. debug returns a ToolMessage, so its
// envelope is never truncated — the scheduler must parse the {type:"debug"}
// marker to trigger the graft, same reason compute does.
var _ toolapi.Tool = (*DebugTool)(nil)
var _ toolapi.TypedExecutor = (*DebugTool)(nil)

// NewDebugTool constructs a DebugTool bound to an Agent.
func NewDebugTool(a *Agent) *DebugTool { return &DebugTool{agent: a} }

func (d *DebugTool) Name() string { return debugToolName }

func (d *DebugTool) Description() string {
	return "Find the ROOT CAUSE of something this run cannot account for. Spawns Holmes, a " +
		"read-only investigator that forms a hypothesis, tests it with its own tool calls, and " +
		"revises it until the cause is established. Two things qualify: a step that FAILED, and " +
		"an OBSERVATION the evidence does not explain — a process touching a file it has no " +
		"obvious reason to touch, an actor that matches no benign shape and no malicious one " +
		"either. Pass what you cannot explain in `problem`, with the exact error text, file " +
		"paths, process names and pids you already have. " +
		"Do NOT use it for transient errors (timeouts, HTTP 5xx, rate limits, empty results) — " +
		"those are retried, not diagnosed — or when the evidence already answers the question. " +
		"Where a fix would need privileges or would change the machine beyond this run, say so in " +
		"`problem` and plan it anyway: the gate decides whether it may proceed, and knowing the cause " +
		"is worth having either way. One debug step per question — plan it as a " +
		"leaf; the next re-plan handles follow-on work once the cause is known."
}

func (d *DebugTool) Impact(params map[string]any) int {
	// What this tool itself does is read: Holmes gathers evidence and names a
	// cause. The write is the microplanner behind it, and that is gated where it
	// is dispatched (dispatchMicroplannerWithRCA) against the rank compute
	// needs — so a run below that rank still gets the diagnosis and simply does
	// not get the fix.
	//
	// It was Affect, which gated the diagnosis on the cost of the repair. Two
	// things followed. An investigation at observe rank could not plan a debug
	// step at all, so the one stage in the engine that forms and tests a
	// hypothesis was unreachable on exactly the runs that needed it. And the
	// reflector, whose tool section IS filtered by rank (toolSectionLines),
	// could not see the tool it is supposed to steer a re-plan towards.
	return toolapi.ImpactObserve
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

// Execute satisfies the Tool interface for callers outside the DAG.
func (d *DebugTool) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(d.ExecuteTyped(ctx, params))
}

/*
 * ExecuteTyped echoes the problem into a {type:"debug"} envelope. The
 * scheduler's tool-completion handler detects this marker and grafts the Holmes
 * investigation parented to this node.
 * param: params - resolved tool params; `problem` is the investigation brief.
 * return: the debug envelope JSON.
 */
func (d *DebugTool) ExecuteTyped(_ context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	problem, _ := params["problem"].(string)
	return toolapi.ToolOK("debug", "", map[string]string{"problem": problem}), nil
}
