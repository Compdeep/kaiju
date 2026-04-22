# The Graph

Every kaiju investigation is a DAG. Components emit nodes, the scheduler fires them in dependency order, the dispatcher resolves data between them and calls tools, the reflector decides whether to keep going. This doc describes each component by its code name and how they compose around the graph.

## Overview

```
                 ┌─────────────┐
                 │   Query     │
                 └──────┬──────┘
                        ▼
                 ┌─────────────┐
                 │  Preflight  │  classifies query → skills, mode, intent
                 └──────┬──────┘
                        ▼
                 ┌─────────────┐
                 │  Executive  │  emits the initial DAG (planStepsToNodes)
                 └──────┬──────┘
                        ▼
    ┌──────────────────────────────────────────────┐
    │  Scheduler                                   │
    │    walks the Graph in topological order      │
    │    fires ready Nodes via the Dispatcher      │
    │    waits on batch completions                │
    └──────┬───────────────────────────────┬───────┘
           │                               │
           ▼                               ▼
    ┌─────────────┐                ┌──────────────┐
    │ Dispatcher  │ → tool.Execute │  Reflector   │ decides:
    │ (per Node)  │                │              │  continue | conclude | investigate
    └─────────────┘                └──────┬───────┘
                                          │ investigate
                                          ▼
                                   ┌──────────────┐
                                   │   Holmes     │  ReAct investigation → RCAReport
                                   └──────┬───────┘
                                          ▼
                                   ┌──────────────┐
                                   │  Debugger /  │  plans fix steps (graft onto Graph)
                                   │ Microplanner │
                                   └──────────────┘
                        ▼ (reflector: conclude)
                 ┌─────────────┐
                 │ Aggregator  │  synthesises the final answer for the user
                 └─────────────┘
```

## Graph data model

`internal/agent/dag.go`.

```go
type Graph struct {
    Nodes        []*Node
    Context      *ContextGate
    ActiveCards  []string        // skills selected by Preflight
    SessionID    string
    // ...
}

type Node struct {
    ID         string
    Tag        string            // short human label
    Type       NodeType          // NodeTool | NodeCompute | NodeReflector | ...
    ToolName   string            // empty for non-tool nodes
    Params     map[string]any
    ParamRefs  map[string]ResolvedInjection  // DI: populated by fireNode before execution
    DependsOn  []string
    Result     string            // JSON or plain text emitted by the tool
    State      NodeState         // StateReady | StateResolved | StateFailed | StateSkipped
    SpawnedBy  string            // parent node when grafted
    StartedAt  time.Time
    EndedAt    time.Time
    // ...
}

type ResolvedInjection struct {
    NodeID   string   // graph node ID of the dep
    Field    string   // dot-path into dep.Result, "" means use whole result
    Template string   // optional "X {{value}} Y" wrap
}
```

Nodes can be grafted onto the Graph at runtime — architect grafts coder/bash children, the microplanner grafts fix steps after Holmes concludes. `SpawnedBy` records the parent so the scheduler can hoist child results back onto the parent.

## Components

### Preflight

`internal/agent/preflight.go`. One LLM call made before the Executive. Classifies the query in a single shot:

```json
{
  "skills":              ["webdeveloper", "python"],
  "mode":                "chat" | "meta" | "investigate",
  "intent":              0 | 1 | 2,
  "required_categories": ["network", "filesystem"],
  "compute_mode":        "deep" | "shallow" | ""
}
```

Outputs flow into the Executive's prompt as context. `mode=chat` or `mode=meta` short-circuits the planner and answers directly via the aggregator.

### Executive

`internal/agent/executive.go`. The top-level planner. Emits the initial DAG as JSON:

```json
{"steps": [
  {"tool": "file_read",  "params": {...}, "depends_on": [],   "tag": "..."},
  {"tool": "edit_file",  "params": {...}, "depends_on": [0],  "tag": "..."},
  {"tool": "bash",       "params": {...}, "param_refs": {...}, "depends_on": [1], "tag": "..."}
]}
```

Prompt is assembled in `buildExecutivePrompt`. Key inputs it sees:

- **Workspace tree** — `WorkspaceTree(5)`, BFS-walked, capped at 120 entries (`scanWorkspaceTree` in `utils.go`).
- **Preflight output** — required tool categories, compute_mode.
- **Tool catalogue** — every registered tool's name, description, and `Parameters()` schema.
- **Skills** — role-specific guidance sections resolved from Preflight's chosen skill cards.

Output is validated by `validatePlanSteps`:
- Unknown tool names → the step is dropped with a log line.
- All gaps, no valid tools → `ExecutiveConversationalError` surfaces the gap text to the user.

The Executive does not re-plan. Recovery from execution failures is the Reflector/Holmes/Microplanner loop below.

### Scheduler

`internal/agent/scheduler.go`. Walks the Graph in topological waves. For each wave:

1. Find all nodes with every dependency resolved.
2. Fire them through the Dispatcher concurrently.
3. Wait for the batch to complete (success or failure).
4. Between waves, inject a Reflector node so the classifier can steer.

The Scheduler also owns:
- **Budget** (`MaxNodes`, `MaxLLMCalls`). Each LLM-bearing node decrements. Exhaustion prunes downstream work.
- **Graft hooks** — compute's architect output spawns setup/coder/execute/service nodes as children. Service starts spawn an auto-grafted health check.
- **Cascade prune** — when a node fails and no Holmes investigation recovers it, its dependent subtree is marked `StateSkipped` so the reflector knows those results never exist.

### Dispatcher

`internal/agent/dispatcher.go` + `dispatcher_validation.go`. The per-node execution layer. Everything a tool call touches passes through here.

Responsibilities, in order:

1. **Tag** the node with the investigation's active skills for frontend display.
2. **Resolve param_refs** — `resolveInjections`. For each declared ref:
   - Look up the dep Node in the graph. Dep must have a non-empty `Result`.
   - Validate presence of the named field in `dep.Result` via `validateParamRef`. If `ref.Field` is "" the whole result is injected as-is; otherwise the field must actually be present. Absent field → fail the step with a descriptive error. (This is what closes the silent-fallback where downstream tools received metadata JSON instead of the intended data.)
   - Extract the value, apply optional `Template`, land it into `n.Params[paramName]`.
3. **Validate direct params** — `validateDirectParams`. Every key in `n.Params` that was not injected via `param_refs` must be either declared in the tool's schema or allowed by `additionalProperties`. Unknown keys (e.g. `bash({command, cwd})` where bash has no `cwd`) → fail the step. Each tool declares its own strictness via its schema; the validator just reads it.
4. **Throttle** — per-tool cooldown via `toolThrottle`.
5. **Gate** — scope check, rate limit, IGX triad check (impact ≤ min(intent, clearance, scope cap)), external clearance if configured.
6. **Dispatch** — `ContextualExecutor.ExecuteWithContext(ec, params)` when implemented (compute, edit_file), plain `Execute(ctx, params)` otherwise.
7. **Audit + side-effect record** — every attempt enters the audit log; non-zero-impact tools land in the event store.
8. **Truncate** result to `maxToolResultLen` with head+tail preservation, except for ContextualExecutor results (those are structured pipeline data the scheduler parses).

Both failure modes — unknown direct param, missing param_ref field — log a `[dispatch:reject]` line. Failures become `StateFailed` on the node; the existing Reflector → Holmes → Microplanner chain picks recovery.

### Reflector

`internal/agent/reflection.go`. Between scheduler waves, or when a batch size threshold is hit, the reflector runs one LLM call that emits:

```json
{
  "decision": "continue" | "conclude" | "investigate",
  "progress": "productive" | "diminishing",
  "summary":  "...",
  "verdict":  "...",           // only on conclude
  "problem":  "...",           // only on investigate
  "aggregate": true | false     // only on conclude
}
```

Inputs the reflector sees come via `ContextGate`:
- **Node Results** — resolved and failed nodes' outputs, gate-filtered.
- **Execution Timeline** — worklog entries, recent-first.
- **Previous Debug Attempts** — any prior microplanner summaries, so a stalled loop is visible.

Decisions steer the scheduler:
- `continue` → fire the next wave.
- `conclude` → stop scheduling. If `aggregate=true`, the Aggregator runs; otherwise `verdict` is the final answer verbatim.
- `investigate` → spawn a Holmes node with `problem` as the investigation brief.

`progress` is a scheduler-consumed signal. Two consecutive `diminishing` waves downgrade `investigate` to `conclude` so Holmes cycles stop being spawned when fixes aren't moving the validator set. `productive` (the default when unsure) resets the streak — unchanged behaviour.

### Holmes

`internal/agent/rca.go`. A ReAct investigator. Iterates up to `MaxHolmesIters` (default 5). Each iteration runs the executor model with Watson's prior scratch + the problem statement and can:

- Use any tool in scope (file_read, bash, web_fetch, etc.) to gather evidence.
- Declare `conclude=true` when it believes the root cause is named.

On conclusion (either voluntary or budget-exhausted) it produces an `RCAReport`:

```go
type RCAReport struct {
    RootCause         string
    Evidence          []string
    Confidence        string
    SuggestedStrategy string
    AffectedFiles     []string   // populated when Holmes sees a pattern spanning multiple files
}
```

Budget-exhausted conclusions get a canned `SuggestedStrategy` marking the investigation as halted so downstream planners can treat the hypothesis as provisional.

### Debugger / Microplanner

`internal/agent/microplanner.go`. When Holmes reports an RCAReport, the Debugger plans the fix. Inputs include the RCA, the current blueprint (if any), the worklog, and the current workspace tree.

Emits a `{"summary": ..., "nodes": [...]}` block. The nodes are regular Graph steps — same shape the Executive emits — and get grafted onto the Graph. They typically include:

- **edit_file** steps for each affected file (one coder call per path — Holmes's `AffectedFiles` drives the fan-out).
- **bash** verification (curl the endpoint, run a test, parse output).
- **service restart** when the fix changes a long-running process.

The Debugger explicitly does NOT plan via compute for known-path edits — edit_file is the authoritative channel. Compute is reserved for value generation (see Compute subsystem below).

### Aggregator

`internal/agent/aggregator.go`. Final LLM call. Synthesises the user-facing answer from the graph's Node Results and worklog. Two modes:

- **executor** (default) — one-shot summary.
- **reasoning** — invoked when the reflector sets `aggregate=true` with a request for a more considered answer.

The aggregator cannot call tools. Everything it writes is synthesis of prior node outputs.

## Compute subsystem

`internal/agent/compute.go` + `builtin_compute.go` + `builtin_edit_file.go`. Handles LLM-driven code generation.

### Two tools, one pipeline

```
Executive picks:        compute            edit_file
                           │                    │
                           │                    │
                     ┌─────┴──────┐             │
                     │            │             │
                     ▼            ▼             ▼
                 mode=deep    mode=shallow   shallow + task_files (always)
                     │            │             │
                     ▼            ▼             ▼
                ARCHITECT      CODER          CODER
                     │         (one call)    (one call, file-bound)
                     ▼
                tasks[] → coder grafts (each a shallow-mode coder call)
```

- **compute(deep)** — project scaffolding. Architect plans, scheduler grafts setup/coders/execute/service/validators.
- **compute(shallow)** — value generation. Coder emits a runnable script, scheduler grafts an exec bash child, the script's stdout is merged onto the parent's Result as `.output` for downstream `param_refs`. Must declare `execute`; a shallow compute that produces a file with no executable command is rejected so `.output` is always meaningful.
- **edit_file** — known-path file operations. `task_files` is required. The Coder pipeline runs the same way as compute(shallow) but with an authoritative destination path; the coder's chosen filename is ignored. No `.output`, no execute — this tool writes files, it does not compute values.

### Architect

LLM call (reasoning model). Sees: goal, workspace scan, function map (regex-extracted function boundaries, 20 languages), interfaces hint, existing blueprints, worklog, skill-card architect guidance.

Returns:

```json
{
  "blueprint":    "... markdown ...",
  "project_root": "project/<name>",
  "interfaces":   { ... },
  "schema":       { ... },
  "setup":        ["npm install", "..."],
  "tasks":        [{"goal": "...", "task_files": ["..."], "brief": "...", "execute": "...", "depends_on_tasks": []}],
  "services":     [{"name": "...", "command": "...", "workdir": "...", "port": 4000}],
  "validation":   [{"name": "...", "check": "..."}]
}
```

Each task is REQUIRED to have `task_files` (enforced by the architect's schema at `function_calls.go:345`). There is no filename-hallucination path in deep mode.

### Coder

LLM call (executor model). Two output shapes:

- **Edit mode** (`task_files` pre-populated AND file exists) — returns `{language, filename, edits: [{old_content, new_content}]}`. Edits are applied via `ApplyFileEdits` (exact text match only — no fuzzy/trimmed fallback).
- **Write mode** — returns `{language, filename, code, execute?, validation?}`. Full content written to disk.

Destination resolution order:
1. `task_files[0]` if set (architect or edit_file path).
2. Coder's `filename` field prefixed with the project root (`projectPrefix` logic).

`projectPrefix` consults the graph's architect-declared `ProjectRoot` first, then a common prefix of `task_files`, then falls back to `"project/"`.

### edit_file vs file_write vs compute

```
edit_file    LLM edits/creates a known file.  task_files REQUIRED.
             Result: {files_edited, edit_count, code_path, language}.
             No .output.

file_write   Byte-writer. path + content. No LLM.
             Use when the exact bytes are already in hand.

compute      Value generator via runnable script (shallow) or project
             scaffold (deep). Emits .output on shallow. NEVER used for
             known-path file edits (use edit_file).
```

## DI and validation

Param flow from plan time to execution:

```
planner declares:            {"params": {"path": "..."},
                              "param_refs": {"content": {"step": 3, "field": "output"}}}
                                        │
                                        ▼
Scheduler builds Node:       Node{Params: {"path": "..."}, ParamRefs: {"content": {NodeID: "n3", Field: "output"}}}
                                        │
                                        ▼
Dispatcher.fireNode:         resolveInjections(n, graph)
                               for each ref:
                                 dep := graph.Get(ref.NodeID)
                                 validateParamRef(paramName, ref, dep.Result)   ← field must exist
                                 value := extractJSONField(dep.Result, ref.Field)
                                 n.Params[paramName] = value
                             validateDirectParams(tool, n.Params, n.ParamRefs)  ← unknown keys → fail
                                        │
                                        ▼
tool.Execute(ctx, n.Params)
```

Both validation points fail the node with a descriptive error. The failure flows through the normal path: reflector → Holmes → microplanner replan. No new recovery machinery.

Validation rules:

- **Direct params** — a key is allowed if it appears in `tool.Parameters().properties` OR the schema's `additionalProperties` is true (default when absent, per JSON Schema). Tools with `additionalProperties: false` get strict checking; tools like `compute` that legitimately accept arbitrary param names via `param_refs` stay loose.
- **Param_refs** — `ref.Field == ""` injects the whole `dep.Result` (legitimate for chaining raw text). Otherwise the field must exist in the dep's Result JSON — no silent fallback to the full result.

Both rejection paths emit `[dispatch:reject]` log lines so telemetry can count hallucination rate without scraping.

## Budgets

Every LLM-bearing component decrements a counter in `Budget`. `MaxLLMCalls` is the global cap. Exhausting it before:

- Reflector → a canned "budget exhausted" summary is written; no more waves.
- Holmes → the investigation is marked halted with a provisional hypothesis (`SuggestedStrategy` says so) and hands off to the Debugger anyway.
- Aggregator → the last reflector `verdict` is surfaced verbatim.

`MaxNodes` caps the graph size. When a graft would push past it, the scheduler logs "budget exhausted, truncating plan" and skips the graft — partial completion over runaway spawn.

## Relevant source

| file | responsibility |
|---|---|
| `internal/agent/dag.go` | Graph, Node, NodeState, topological ordering |
| `internal/agent/scheduler.go` | wave execution, graft hooks, budget, cascade prune |
| `internal/agent/dispatcher.go` | per-node execute: injection, throttle, gate, dispatch, audit |
| `internal/agent/dispatcher_validation.go` | `validateDirectParams`, `validateParamRef`, `parseToolSchema` |
| `internal/agent/preflight.go` | one-shot classifier |
| `internal/agent/executive.go` | initial DAG planner |
| `internal/agent/reflection.go` | between-wave classifier |
| `internal/agent/rca.go` | Holmes ReAct investigator + RCAReport |
| `internal/agent/microplanner.go` | debugger/microplanner fix planner |
| `internal/agent/aggregator.go` | final answer synthesis |
| `internal/agent/compute.go` | runCompute, computePlan, computeCode |
| `internal/agent/builtin_compute.go` | ComputeTool schema + dispatch wrapper |
| `internal/agent/builtin_edit_file.go` | EditFileTool — task_files-required wrapper over the coder |
| `internal/agent/contextgate.go` | source selection for LLM prompt assembly |
