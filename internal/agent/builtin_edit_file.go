// Package agent — builtin_edit_file.go.
//
// edit_file is the LLM-backed file operation tool. It owns every workflow
// where the Executive (or microplanner/architect) needs an LLM to decide
// what to write into a specific, named file — both modifying an existing
// file and creating a new one at a known path. It is the authoritative
// channel for "work on project/foo/bar.py via the Coder."
//
// Why this exists as its own tool (and not a mode of compute):
//
//   - task_files is REQUIRED here and validated at schedule time. The
//     shared-tool design made task_files optional on compute, letting the
//     Coder hallucinate filenames when the Executive forgot to set it.
//     That silent hallucination caused the brace_json-style file clobber.
//     Separating tools turns the path from a hint into a contract.
//
//   - The Result schema is stable: {files_edited, code_path, language}.
//     No conditional .output field. Downstream param_refs cannot ask for
//     something this tool never produces, so a wire-mismatch becomes a
//     loud error instead of a silent fallback.
//
//   - The Executive's decision surfaces at tool-choice time, not buried
//     inside a params-shape heuristic. "I need an LLM to touch this
//     known file" is answered by picking edit_file.
//
// Delegation model: edit_file is a thin wrapper that builds the same
// params compute-shallow would consume and calls agent.runCompute. The
// Coder pipeline (prompt assembly, edit parsing, ApplyFileEdits,
// projectPrefix enforcement, workspace safety, worklog logging) is
// reused as-is — edit_file never reimplements any of it.
//
// What this tool does NOT do:
//   - No script execution (no "execute" field on Result).
//   - No scratch-file creation (no "Coder picks a filename" fallback).
//   - No project scaffolding (that's compute(deep) via architect).
//
// Schema (required fields enforced at schedule time):
//   task_files []string — REQUIRED, at least one entry.
//   goal       string   — REQUIRED, what the Coder should do.
//   language   string   — optional, auto-detected from extension otherwise.
//   context    object   — optional, upstream data injected via param_refs.
//   hints      []string — optional, prior failure messages.

package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Compdeep/kaiju/internal/agent/tools"
)

// EditFileTool is the registered tool entry for edit_file.
//
// Implements both the standard Tool interface (for registry/schema
// visibility to the Executive) and ContextualExecutor (the real execution
// path — the Coder pipeline needs graph, budget, and LLM clients that
// plain Execute cannot carry). The dispatcher always prefers
// ExecuteWithContext when a tool implements it, so the plain Execute
// method below is a defensive stub that should never be reached.
type EditFileTool struct {
	agent *Agent
}

// Compile-time interface assertions.
var _ tools.Tool = (*EditFileTool)(nil)
var _ ContextualExecutor = (*EditFileTool)(nil)

// NewEditFileTool constructs an EditFileTool bound to an Agent. The agent
// reference gives ExecuteWithContext access to the LLM clients, workspace,
// and the runCompute entry point that hosts the Coder pipeline.
func NewEditFileTool(a *Agent) *EditFileTool { return &EditFileTool{agent: a} }

func (e *EditFileTool) Name() string { return "edit_file" }

func (e *EditFileTool) Description() string {
	return "Edit or create a specific file via the Coder LLM. Use this when " +
		"you know which file to operate on (e.g., 'fix project/api/server.js', " +
		"'add a route to project/api/routes.py', 'create project/cfg/app.yaml'). " +
		"task_files is REQUIRED — it names the file(s) to work on. For data " +
		"generation without a known file target, use compute instead. For " +
		"writing bytes already in hand, use file_write."
}

// Impact reports a file-affecting side effect (ImpactAffect). edit_file
// always writes to disk when it runs.
func (e *EditFileTool) Impact(params map[string]any) int {
	return tools.ImpactAffect
}

// editFileParamSchema is the wire schema shown to the Executive. task_files
// is marked required so the LLM's tool-definition view enforces it at
// tool-call time; a runtime check in ExecuteWithContext is the belt-and-
// braces backstop for the inevitable case where the LLM ignores "required".
var editFileParamSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"task_files": {
			"type": "array",
			"items": {"type": "string"},
			"description": "The file path(s) to edit or create. The Coder receives each existing file's current content and emits text-match edits; for a path that does not yet exist the Coder emits full content. At least one path is required."
		},
		"goal": {"type": "string", "description": "What the Coder should do to the file (e.g., 'add CORS middleware', 'fix the missing comma on line 11', 'implement the health endpoint')."},
		"language": {"type": "string", "description": "Optional language hint. Auto-detected from the file extension if omitted."},
		"context": {"type": "object", "description": "Optional data from upstream steps (injected via param_refs). Handed to the Coder as reference material."},
		"hints": {"type": "array", "items": {"type": "string"}, "description": "Optional error messages from previous failed attempts on this file."}
	},
	"required": ["task_files", "goal"]
}`)

func (e *EditFileTool) Parameters() json.RawMessage {
	return editFileParamSchema
}

// editFileOutputSchema documents the stable Result shape. Unlike compute,
// edit_file NEVER produces an "output" field — it does not execute code.
// Downstream steps cannot param_ref a field that doesn't exist here.
var editFileOutputSchema = json.RawMessage(`{
	"type": "object",
	"description": "Result of the edit/create operation. No conditional fields — the tool never produces script stdout and never runs code.",
	"properties": {
		"files_edited":  {"type": "array", "items": {"type": "string"}, "description": "Paths modified or created."},
		"edit_count":    {"type": "integer", "description": "Number of text-match edits applied (0 when a fresh file was written)."},
		"code_path":     {"type": "string", "description": "Absolute on-disk path of the file that was written."},
		"language":      {"type": "string", "description": "Language declared by the Coder."},
		"type":          {"type": "string"}
	}
}`)

func (e *EditFileTool) OutputSchema() json.RawMessage { return editFileOutputSchema }

// Execute is a defensive stub. edit_file requires graph context, LLM
// clients, and the workspace that plain (ctx, params) cannot carry. The
// dispatcher always prefers ExecuteWithContext when the tool implements
// ContextualExecutor, so this path is unreachable in normal use.
func (e *EditFileTool) Execute(_ context.Context, _ map[string]any) (string, error) {
	return "", fmt.Errorf("edit_file requires graph context; invoke via dispatcher")
}

// ExecuteWithContext validates the required params and delegates to the
// Coder pipeline hosted by runCompute. The flow is:
//
//  1. Validate task_files is non-empty. If the Executive omitted it, fail
//     loudly rather than letting the Coder hallucinate a filename — this
//     is the fix for the brace_json silent-clobber bug.
//  2. Rebuild the params map as a shallow-compute invocation with
//     mode="shallow" and task_files set. All other edit_file params
//     (goal, language, context, hints) pass through verbatim.
//  3. Call agent.runCompute. That hits the same code path compute-shallow
//     uses, so projectPrefix enforcement, ApplyFileEdits, edit logging,
//     and worklog writes all behave identically. The result string is
//     returned to the dispatcher unmodified.
//
// The "execute" and "service" fields that compute-shallow accepts are
// intentionally NOT supported here — edit_file's contract is
// file-modification-only. If the Coder chooses to emit an execute field
// anyway, runCompute ignores it because edit_file never passed one in.
func (e *EditFileTool) ExecuteWithContext(ec *ExecuteContext, params map[string]any) (string, error) {
	taskFiles, err := extractTaskFiles(params)
	if err != nil {
		return "", err
	}
	if len(taskFiles) == 0 {
		return "", fmt.Errorf("edit_file requires at least one entry in task_files (got empty list)")
	}
	goal, _ := params["goal"].(string)
	if goal == "" {
		return "", fmt.Errorf("edit_file requires a non-empty goal")
	}

	computeParams := map[string]any{
		"goal":       goal,
		"mode":       "shallow",
		"task_files": taskFiles,
	}
	// Pass through the optional-but-useful fields that the Coder knows how
	// to consume. Anything else on params is silently dropped so edit_file
	// stays a narrow contract.
	for _, passthrough := range []string{"language", "context", "hints", "query"} {
		if v, ok := params[passthrough]; ok {
			computeParams[passthrough] = v
		}
	}

	return e.agent.runCompute(ec, computeParams)
}

// extractTaskFiles normalises the task_files param into []string,
// accepting both []any (JSON-unmarshalled shape) and []string (programmatic
// callers). Non-string entries are dropped. Empty strings are dropped.
func extractTaskFiles(params map[string]any) ([]string, error) {
	raw, ok := params["task_files"]
	if !ok || raw == nil {
		return nil, fmt.Errorf("edit_file requires task_files")
	}
	switch v := raw.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, f := range v {
			if s, ok := f.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	case []string:
		out := make([]string, 0, len(v))
		for _, s := range v {
			if s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("edit_file: task_files must be a string array, got %T", raw)
	}
}
