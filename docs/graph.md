# The Graph

Every kaiju investigation is a DAG. Components emit nodes, the scheduler fires them in dependency order, the dispatcher resolves data between them and calls tools, the reflector decides whether to keep going. This doc describes each component by its code name and how they compose around the graph.

## Overview

```
                 ┌─────────────┐
                 │   Query     │
                 └──────┬──────┘
                        ▼
                 ┌───────────────────────┐
                 │  routeQuery (ROUTE)    │  cheap 16-token forced tool call, temp 0
                 │  chat / meta /         │  fails safe to CHAT on any error/refusal
                 │  investigate           │
                 └──────┬────────────────┘
             chat/meta  │  investigate
             (short-    │
              circuit)  ▼
                 ┌───────────────────────┐
                 │  classifyInvestigate  │  skills · mode · intent ·
                 │  (preflight)          │  required_categories · context · compute_mode
                 └──────┬────────────────┘
                        ▼
                 ┌─────────────┐
                 │  Executive  │  native planner — pinned to emit plan()
                 └──────┬──────┘  (the structured-JSON planner mode was removed)
                        ▼
    ┌──────────────────────────────────────────────┐
    │  Scheduler                                    │
    │    walks the Graph in dependency batches        │
    │    fires ready Nodes via the Dispatcher       │
    │    injects a Reflector between batches           │
    └──────┬───────────────────────────────┬───────┘
           │                               │
           ▼                               ▼
    ┌─────────────┐                ┌──────────────┐
    │ Dispatcher  │ → tool.Execute │  Reflector   │ decides (3-way):
    │ (per Node)  │                │              │  continue | replan | conclude
    └─────────────┘                └──────┬───────┘
                                          │
              ┌───────────────────────────┼───────────────────────────┐
              │ continue                  │ replan                     │ conclude
              ▼                           ▼                            ▼
        fire next batch          Executive re-plans               Aggregator
                                (fresh replan frame)         (or verdict verbatim)
                                          │
                    ┌─────────────────────┴─────────────────────┐
                    │ a SUCCESS revealed the next move → new     │
                    │   steps                                    │
                    │ a step FAILED → the Executive plans a      │
                    │   `debug` step ↓                           │
                    └─────────────────────┬─────────────────────┘
                                          ▼
                                   ┌──────────────┐
                                   │ debug (tool) │  scheduler grafts Holmes →
                                   └──────┬───────┘
                                          ▼
                                   ┌──────────────┐
                                   │   Holmes     │  read-only root-cause of the FAILED step
                                   └──────┬───────┘
                                          ▼
                                   ┌──────────────┐
                                   │ Microplanner │  RCA → fix steps (grafted onto Graph)
                                   └──────────────┘
```

Repair is not a separate lane. A failure flows through the *same* door as an expansion: the reflector returns `replan`, the Executive plans a `debug` super-tool step, and when that node resolves the scheduler grafts the first Holmes iteration onto it. There is no `investigate → Holmes → Debugger` shortcut anymore.

## Graph data model

`internal/agent/dag.go`.

```go
type Graph struct {
    Nodes        []*Node
    Context      *ContextGate
    Preflight    *PreflightResult // per-investigation classify output
    ActiveCards  []string         // skills selected by classifyInvestigate
    SessionID    string
    // ...
}

type Node struct {
    ID         string
    Tag        string            // short human label
    Type       NodeType          // NodeTool | NodeCompute | NodeReflection | NodeHolmes | ...
    ToolName   string            // empty for non-tool nodes
    Params     map[string]any    // ${node.<id>(.field)?} placeholders inside string values are substituted by the dispatcher before execution
    DependsOn  []string
    Result     string            // JSON or plain text emitted by the tool
    State      NodeState         // StatePending | StateRunning | StateResolved | StateFailed | StateSkipped
    SpawnedBy  string            // parent node when grafted
    StartedAt  time.Time
    EndedAt    time.Time
    // ...
}
```

The `NodeType` enum is `NodeTool | NodeCompute | NodeExecutive | NodeMicroPlanner | NodeAggregator | NodeActuator | NodeReflection | NodeObserver | NodeInterjection | NodeHolmes`.

Dependency injection is expressed inline as `${step.N(.field)?}` placeholders inside string-valued params. The Executive emits them at plan time; `planStepsToNodes` rewrites them to `${node.<id>(.field)?}` once the per-node IDs are minted; the dispatcher substitutes them at fire time from `dep.Result`.

Nodes can be grafted onto the Graph at runtime — the architect grafts coder/bash children, a resolved `debug` node grafts the first Holmes iteration, and the microplanner grafts fix steps after Holmes concludes. `SpawnedBy` records the parent so the scheduler can hoist child results back onto the parent.

## Components

### Preflight

`internal/agent/preflight.go`. Two separable jobs run at the front of every interactive investigation.

**Stage 1 — routeQuery (the cheap first pass).** `routeQuery` fires the `ROUTE` prompt (`prompt.Route`) as a tiny forced `route()` tool call: `ToolChoice: "required"`, `Temperature: 0.0`, `MaxTokens: 16`, on the dedicated route lane (falls back to the executor/light lane — see [model-calls.md](model-calls.md)). It classifies the *latest* message into one mode and nothing else:

```
mode: "chat" | "meta" | "investigate"
```

It carries only a minimal slice of history (`routeContext` — the running conversation summary, if any, plus the previous turn) so a terse follow-up like "try again" inherits the nature of the turn it continues. **It fails safe to `chat`**: on any classifier failure — the model errored, refused, or returned unparseable output — `routeQuery` returns `"chat"`, which the ROUTE prompt itself calls the default. Escalating to the agent is a positive decision, not what happens when the router trips (an aligned model that balks at edge content was wrongly forced onto the agent path when the fallback was `investigate`).

`chat` and `meta` short-circuit here — the scheduler skips the classify + skill-manifest cost and the executive entirely (`ExecutiveConversationalError`). Only `investigate` pays for Stage 2. (Autonomous mode skips routing entirely — its result would only be discarded — and runs Stage 2 directly with `mode` forced to `investigate`.)

**Stage 2 — classifyInvestigate (the plan-prep pass).** On `investigate`, one executor-model call runs the `PREFLIGHT` prompt and emits the structured metadata the planner and downstream components consume:

```json
{
  "skills":              ["webdeveloper", "python"],
  "mode":                "chat" | "meta" | "investigate",
  "intent":              "<intent name from the registry>",
  "required_categories": ["network", "filesystem", "compute", "process", "info"],
  "context":             "paragraph quoting every concrete identifier (URLs, paths, selectors, constants) verbatim",
  "compute_mode":        "" | "shallow" | "deep"
}
```

The skill manifest is built here (only reached on the investigate path). `intent` resolves against the intent registry; `context` is the ONLY channel by which concrete identifiers from chat history reach the Coder/Executive/microplanner, so the prompt forces verbatim quoting. Any missing or malformed field falls back to a safe default (`validatePreflight` / `defaultPreflight`). The result lands on the Graph (`graph.Preflight`, `graph.ActiveCards`) — per-investigation, so concurrent runs never clobber each other.

### Executive

`internal/agent/executive.go`. The top-level planner. It runs via **native function calling only** — the old structured-JSON planner mode was removed (modern models all support native tool calling and it parses more reliably). The model is **pinned to emit a single `plan()` tool call** (`ToolChoice: llm.ForceToolChoice("plan")`), not "call some tool" — a weak reasoning model otherwise emits a bare `web_search`/`web_fetch` call and hard-fails. The plan is the entire DAG as the tool argument:

```json
{"steps": [
  {"tool": "file_read",  "params": {"path": "config.json"},                                "depends_on": [],   "tag": "..."},
  {"tool": "edit_file",  "params": {"goal": "...", "task_files": ["..."]},                 "depends_on": [0],  "tag": "..."},
  {"tool": "bash",       "params": {"command": "echo '${step.0.content}' | wc -l"},        "depends_on": [1],  "tag": "..."}
]}
```

Prompt is assembled in the executive system-prompt path. Key inputs it sees:

- **Workspace tree** — `WorkspaceTree(5)`, BFS-walked, capped at 120 entries (`scanWorkspaceTree` in `utils.go`).
- **Preflight output** — the `context` paragraph, required tool categories, `compute_mode`, inferred intent.
- **Tool catalogue** — every registered tool's name, description, and `Parameters()` schema (plus output schemas so `${step.N.field}` placeholders can be wired against correct result shapes).
- **Skills** — role-specific guidance sections resolved from the classify pass's chosen skill cards.

Output is validated by `validatePlanSteps`:
- Unknown tool names → the step is dropped with a log line.
- All gaps, no valid tools → `ExecutiveConversationalError` surfaces the gap text to the user.

Broken `${step.N.field}` wiring is caught separately by `validatePlanEdges` *before* the plan runs — one re-plan carrying the exact fault, rather than a doomed run. It sits alongside the parse-fail and hallucinated-tool retries as an executive repair round; see [Edges — anti-fabrication layer](#edges--anti-fabrication-layer).

**The Executive DOES re-plan.** When the reflector returns `replan`, the scheduler re-invokes `runExecutive` with a **replan frame** appended to the user query. That frame is a fresh sandbox: prior results have ALREADY FINISHED and are pasted into the worklog as literal DATA, so the new plan must reference those values *literally* (paste the actual URL/text) and set `depends_on: []` for anything already done. Crucially, `${step.N}` and `depends_on:[N]` indices in a replan are **plan-local** — they address only the NEW steps in *this* plan, never the concluded ones. `planStepsToNodes` / `rewriteStepTemplates` enforce this: an index that would point at a prior-frame node (or at the node itself) is left as an unresolved placeholder rather than wired into a dead/self edge (the old bug where a stale `depends_on:[0]` made a node wait on itself, get skipped, and re-plan the same fetch forever). If the replan move is to fix a failure, the frame instructs the Executive to plan a single `debug` step (a leaf) carrying the failure text.

### Scheduler

`internal/agent/scheduler.go`. Walks the Graph in topological batches. For each batch:

1. Find all nodes with every dependency resolved.
2. Fire them through the Dispatcher concurrently.
3. Wait for the batch to complete (success or failure).
4. Between batches, inject a Reflector node so the classifier can steer.

Reflection *timing* depends on the DAG mode (`dag.go`: `DAGModeReflect` / `DAGModeNReflect` / `DAGModeOrchestrator`):
- **reflect** — reflections are structural (forced serialization; the reflector gates downstream batches).
- **nReflect** — true DAG scheduling; a reflection is injected every `BatchSize` tool completions.
- **orchestrator** — true DAG scheduling plus a lightweight per-node observer LLM spawned after each tool completes.

The Scheduler also owns:
- **Budget** (`MaxNodes`, `MaxLLMCalls`, plus replan/debug round caps `maxReplans` / `maxInvestigations`). Each LLM-bearing node decrements. Exhaustion prunes downstream work.
- **Graft hooks** — the architect's compute output spawns setup/coder/execute/service nodes as children; a service start spawns an auto-grafted health check; a resolved **`debug`** node grafts the first Holmes iteration (`spawnFirstHolmes`); a concluded Holmes grafts the **microplanner**, whose fix steps are grafted in turn (and validators are re-grafted after them).
- **Cascade prune** — when a node fails and no debug cycle recovers it, its dependent subtree is marked `StateSkipped` so the reflector knows those results never exist.
- **Diminishing-returns brake** — two consecutive reflector `diminishing` rounds downgrade a `replan` to `conclude`, so debug/expand batches stop being spawned when fixes aren't moving the answer forward.

### Dispatcher

`internal/agent/dispatcher.go` + `dispatcher_validation.go`. The per-node execution layer. Everything a tool call touches passes through here.

Responsibilities, in order:

1. **Tag** the node with the investigation's active skills for frontend display.
2. **Substitute templates** — `substituteTemplates`. Walk every string value in `n.Params` looking for `${node.<id>(.field)?}` placeholders. For each:
   - Look up the dep Node in the graph. Dep must have a non-empty `Result`.
   - If the field path is empty, the whole `dep.Result` replaces the placeholder. Otherwise the field is extracted from `dep.Result` (parsed as JSON when possible). When the placeholder is the entire string value (`"${node.X.f}"`), the substituted value preserves its original type (object, array, number, string). When embedded mid-string (`"prefix ${node.X.f} suffix"`), the value is stringified.
   - A missing field falls back to the whole result with a `[dispatch:resolve-fallback]` log line so silent corruption is at least audible.
3. **Validate direct params** — `validateDirectParams`. Every key in `n.Params` must be either declared in the tool's schema or allowed by `additionalProperties`. Unknown keys (e.g. `bash({command, cwd})` where bash has no `cwd`) → fail the step. Each tool declares its own strictness via its schema; the validator just reads it.
4. **Throttle** — per-tool cooldown via `toolThrottle`.
5. **Gate** — scope check, rate limit, IGX triad check (impact ≤ min(intent, clearance, scope cap)), external clearance if configured.
6. **Dispatch** — `ContextualExecutor.ExecuteWithContext(ec, params)` when implemented (compute, edit_file, debug), plain `Execute(ctx, params)` otherwise.
7. **Audit + side-effect record** — every attempt enters the audit log; non-zero-impact tools land in the event store.
8. **Truncate** result to `maxToolResultLen` with head+tail preservation, except for ContextualExecutor results (those are structured pipeline data the scheduler parses — including the `{type:"debug"}` envelope that triggers the Holmes graft).

Both failure modes — unknown direct param, malformed template — log a `[dispatch:reject]` line. Failures become `StateFailed` on the node; recovery flows through the reflector `replan` → `debug` → Holmes → microplanner chain below.

### Reflector

`internal/agent/reflection.go`, prompt `=== REFLECTOR ===`. Between scheduler batches (or when a batch threshold is hit), one LLM call classifies the state into **one of three decisions** and emits:

```json
{
  "decision": "continue" | "replan" | "conclude",
  "progress": "productive" | "diminishing",
  "summary":  "one paragraph: what happened, current state, exact error text",
  "next":     "...",          // only on replan — the concrete next move
  "verdict":  "...",          // only on conclude — final answer for the user
  "aggregate": true | false   // only on conclude
}
```

The verdict set is `continue | replan | conclude` — there is no `investigate` decision and no `aggregate` decision. `investigate` was removed; `parseReflectionOutput` coerces any stray `"investigate"` (from an old prompt or model) into `"replan"`, carrying its problem statement into `next`.

Decisions steer the scheduler:
- **continue** — work is still in flight; let the current plan finish.
- **replan** — the graph needs to GROW and the goal isn't answered yet. Two shapes, same decision:
  - *a success revealed the next move* — e.g. searches returned URLs → "fetch the 3 URLs the searches surfaced".
  - *a step FAILED and needs fixing* — describe the failure (exact error text, file paths, module names) in `next`. The Executive then plans a `debug` step; the reflector names the move, the Executive plans HOW.
- **conclude** — the goal is met, OR the request is too vague/underspecified to act on (ask the user to clarify instead of guessing). If `aggregate=false` the reflector's `verdict` is the final answer verbatim; if `aggregate=true` the Aggregator runs. A `conclude` stands: nothing overrules it. What the reflector is holding and never followed is reported to it in the block the reframe prepends, and the decision is its own.

The prompt leans hard on the anti-hallucination rule: conclude ONLY when the evidence *answers* the goal — an unfetched URL or an un-followed lead is `replan`, never a confident guess from memory. Transient tool output (empty fetch, HTTP 5xx, timeout, rate limit), out-of-scope failures, and truly-unfixable environment failures are `conclude`, not `replan` — they don't belong in the debugger.

Inputs the reflector sees (assembled by `assembleReflectorPrompt` via `ContextGate`): **Original Request**, a plain-English **Budget** line ("replan round 2 of 3, debug round 1 of 2, 3m40s elapsed"), a **Graph Summary** (resolved/failed/skipped/pending counts), **Node Results** (gate-filtered), the **Execution Timeline** (worklog, recent-first), and **Previous Debug Attempts** (prior microplanner summaries, so a stalled loop is visible — the next problem description MUST name a DIFFERENT root cause).

`progress` is a scheduler-consumed signal: two consecutive `diminishing` rounds downgrade `replan → conclude`, so debug cycles stop being spawned when fixes aren't moving the state forward. `productive` (the default when unsure) resets the streak.

### Debug super-tool

`internal/agent/builtin_debug.go`. `debug` is the REPAIR super-tool — the Executive-planned door into the failure-handling pipeline. It mirrors `compute`: a thin tool interface over a DAG sub-structure the scheduler grafts.

- **Impact** is `ImpactAffect` (write-capable — the microplanner fix edits files), so IGX gates it like compute; lanes below the required clearance can't invoke it.
- **Params**: `{ "problem": "<exact error text, file paths, module names, what was being attempted>" }` — this is Holmes's investigation brief.
- The tool itself does almost nothing. `ExecuteWithContext` echoes the problem into a `{type:"debug", problem}` envelope. The scheduler's tool-completion handler detects that marker and grafts the first Holmes iteration (`spawnFirstHolmes`) parented to the debug node; from there the `NodeHolmes → NodeMicroPlanner → validator` machinery drives the fix, fully visible in the DAG trace.
- **One debug step per failure**, planned as a *leaf* (no dependents) — the next re-plan handles follow-on work once the fix lands. Independent sibling work keeps running (the graft does not skip pending nodes).
- **No debug-in-debug**: `debug` is pruned from Holmes's and the microplanner's tool lists, and any `debug` step in a microplanner fix plan is dropped — a debug must never spawn another debug.

Repair thus flows through the same door as expansion: `reflect.replan → Executive plans a `debug` step → this graft → Holmes → microplanner`.

### Holmes

`internal/agent/rca.go`, prompt `=== HOLMES ===`. Holmes is a **read-only, Sherlock-style root-cause investigator of a FAILED STEP**. He is agnostic to what kind of work failed — a data fetch, a calculation, a file operation, a service action, a build. He is NOT the query planner and he never answers the user; he sits between the reflector (which classifies the symptom) and the microplanner (which prescribes the fix), and emits a structured root-cause analysis the microplanner consumes as authoritative.

He runs as a ReAct loop, up to `MaxHolmesIters` (default 5). Each iteration is a real graph node (`NodeHolmes`), so the investigation is visible in the DAG trace: a Holmes LLM call picks read-only tools, they run as the next node depending on it, then Holmes fires again on the result. Each iteration can:

- Use any *read-only* tool in scope (file_read, bash, web_fetch, service logs, etc.) to gather evidence. Holmes never writes, restarts, or mutates.
- Declare `conclude=true` when the root cause is named — a *symptom* (a specific error at a specific file) is not a valid conclude; the cause is the upstream configuration/state that made the symptom inevitable, and the prompt requires verifying the upstream layer (bundler config, package.json/install log, .env, process list) before concluding.

**Step 0 — is there a case at all?** Before iterating, Holmes scans the problem statement and fast-exits on iteration 1 (confidence `low`) if the "failure" isn't a real, in-scope, fixable bug:

- **Out of scope** — the failure is in the agent's own infrastructure (`cmd/`, `internal/`, `.kaiju/`, an absolute system path) rather than the user's task.
- **Transient tool** — empty/null from web_fetch/web_search, HTTP 5xx, timeout, rate limit → "retry/skip recommended".
- **No crime** — no concrete error, no FAIL/ERROR tags, no explicit request to debug.
- **Internal Kaiju plumbing** — the problem references `${step.N…}`, `depends_on`, `dispatch:reject`, `validator`, `template substitution`, etc. — a malformed-plan complaint, not a real-world bug.

On conclusion (voluntary or budget-exhausted) it produces an `RCAReport`:

```go
type RCAReport struct {
    RootCause         string
    Evidence          []string
    Confidence        string     // "high" | "medium" | "low"
    SuggestedStrategy string     // a concrete one-sentence fix direction — a pointer, not a patch
    AffectedFiles     []string   // every file likely to carry the same pattern
}
```

When the root cause is a *pattern* that likely repeats across sibling files (an export-style mismatch across router modules, a missing `type: module` across a directory), Holmes lists every affected file in `AffectedFiles` so the debugger batches the fix — one investigation per error class, not one per symptom. A budget-exhausted conclusion gets a canned `SuggestedStrategy` marking the investigation as halted so the microplanner can treat the hypothesis as provisional.

### Debugger / Microplanner

`internal/agent/microplanner.go`, prompt `=== MICROPLANNER ===`. When Holmes concludes, the scheduler grafts the microplanner (a clean-room "debugger") to translate the RCA into a fix. It treats Holmes's `root_cause` and `evidence` as authoritative and does NOT re-diagnose. Inputs include the RCA, the current blueprint (if any), the worklog, and the workspace tree.

Emits a `{"summary": ..., "nodes": [...]}` block. The nodes are regular Graph steps — same shape the Executive emits — and get grafted onto the Graph as children of the microplanner node. They typically include:

- **edit_file** steps for each affected file (one coder call per path — Holmes's `AffectedFiles` drives the fan-out; same-class errors are batched).
- **bash** verification (curl the endpoint, run a test, parse output).
- **service restart** when the fix changes a long-running process.

Any `debug` step in the plan is dropped (no debug-in-debug). The microplanner explicitly does NOT plan via compute for known-path edits — edit_file is the authoritative channel; compute is reserved for value generation (see Compute subsystem below). After the fix nodes graft, the scheduler re-grafts any stored validators to prove the fix worked.

### Aggregator

`internal/agent/aggregator.go`, prompt `=== AGGREGATOR ===`. The final LLM call. It synthesises the user-facing answer from the graph's Node Results and worklog and cannot call tools — everything it writes is synthesis of prior node outputs.

Whether it runs, and on which lane, is driven by `agg_mode` (`-1` auto / `0` skip / `1` executor / `2` reasoning). In auto (`-1`) the lane is chosen at preflight by `decideAutoAggMode` over structural signals (`NeedsSynthesis`, fanout, evidence, compute), not by the model at the end — see [Edges — anti-fabrication layer](#edges--anti-fabrication-layer):

- When the reflector concluded with `aggregate=false`, the aggregator is **skipped** and the reflector's `verdict` is the final answer verbatim.
- Otherwise it runs on the **reasoning (heavy) lane by default**. `aggregate=true` from the reflector routes here explicitly (`agg_mode=2`).
- The **executor lane** is used only when `agg_mode=1` — including the forced case where the graph contains compute nodes (compute runs always need the aggregator for a properly formatted response).

The aggregator is exempt from the budget — it always runs to give the user a response — and the prompt forbids inventing data, narrating prior-run actions as if they happened now, or passing internal Kaiju errors through to the user.

## Edges — anti-fabrication layer

An *edge* frames a hand-off between two steps: it gets the previous step's output ready to be the next step's input. It FRAMES — it does not verify or police. A **code edge** is structural (it reads the shape of what ran — envelope Status signals, failed nodes, declared output schemas — and interprets no meaning); an **LLM edge** reframes that structural fact against the request in words. Kaiju layers several such edges over the graph whose one job is to keep a run from *fabricating* — inventing a URL, vouching for a source it never read, concluding from memory. Every one is **gated**: it inspects the structural signal first and returns `""` when the run is clean, so the common path pays nothing. Where an edge calls an LLM it **fails open** to the structural note alone — a reframe never blocks the run.

Two of these — `coverageEdge` and `groundingEdge` — were removed. Each added a small model call to the reflector, both fired on the same condition so neither could fire alone, and both prepended a block to the same prompt: three calls to answer one question, with the second and third describing the same situation twice. The grounding one also spoke in one application's vocabulary, telling every run it had "real URLs from a search" whatever its tools actually returned.

What survives of them is what the scheduler reads rather than what a model was told. The block the reframe prepends reports what a run is holding and never followed, and the reflector decides what to do about it; nothing overrules that decision.

- **`validatePlanEdges`** (`executive.go`) — runs at plan time, BEFORE any node fires, over every `${step.N.field}` reference in the plan. It rejects the three ways the planner mis-wires a hand-off, generically for any tool, from the declared output schemas:
  - **self-reference** — a step reads its own output (`${step.i…}` inside step `i`); a step cannot consume itself.
  - **out-of-range** — the index points at a step the plan does not contain.
  - **wrong-producer** — a top-level field the producing tool never emits, classically `${step.N.results.0.url}` off a `web_fetch` when a `results[]` list only comes from `web_search`. Only a tool that DECLARES an output schema is checked, and only its top-level field (`fieldExistsInSchema`), so an incomplete deep schema can't false-reject.
  Any broken edge becomes **one re-plan** carrying the exact fault as feedback, rather than a doomed run — a self/out-of-range ref would leak its literal placeholder into the tool (an "invalid URL …"), and a wrong-producer ref would dangle a dead edge the reflector then re-plans forever.


- **`NeedsSynthesis` / `decideAutoAggMode`** — the aggregator-mode decision (see Aggregator above) is made at **preflight over structural signals**, not left to the model at the end. `NeedsSynthesis` is a preflight flag ("this run must end with a written synthesis"); a run also counts as *complex* when it structurally fanned out to ≥ `complexFanoutFloor` (6) resolved tool nodes. In auto mode (`agg_mode -1`), `decideAutoAggMode` then picks the lane purely over `{compute, complex, evidence, reflector-wants}`:
  - **compute present** → executor lane (a compute run always needs a formatted answer).
  - **complex + usable evidence** → reasoning model (a real synthesis, with the honesty framing).
  - **complex + NO usable evidence** → **skip**, so the reflector's honest "couldn't get the data" verdict stands rather than an aggregator pass over emptiness — the reflector's override.
  - simple + reflector wants it → reasoning model; simple + reflector done → skip.

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
- **compute(shallow)** — value generation. Coder emits a runnable script, scheduler grafts an exec bash child, the script's stdout is merged onto the parent's Result as `.output` so downstream steps can read it via `${step.N.output}`. Must declare `execute`; a shallow compute that produces a file with no executable command is rejected so `.output` is always meaningful.
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

Each task is REQUIRED to have `task_files` (enforced by the architect's schema at `function_calls.go`). There is no filename-hallucination path in deep mode.

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
planner declares:            {"params": {"path": "...",
                                         "content": "${step.3.output}"}}
                                        │
                                        ▼
planStepsToNodes rewrites:   {"params": {"path": "...",
                                         "content": "${node.n3.output}"}}
                                        │
                                        ▼
Dispatcher.fireNode:         substituteTemplates(n, graph)
                               walk every string value in n.Params:
                                 for each ${node.<id>(.field)?} match:
                                   dep := graph.Get(<id>)
                                   if value is the entire string → replace with typed dep field
                                   else → embed stringified field mid-string
                             validateDirectParams(tool, n.Params)  ← unknown keys → fail
                                        │
                                        ▼
tool.Execute(ctx, n.Params)
```

Substitution failures (referenced node not in graph, dep has no `Result`) fail the node with a descriptive error. Missing-field paths fall back to the whole `dep.Result` with a `[dispatch:resolve-fallback]` log line. Failures flow through the normal path: reflector `replan` → a `debug` step → Holmes → microplanner. No new recovery machinery.

On a **replan**, `${step.N}` and `depends_on:[N]` indices are plan-local — they address only the NEW steps in that plan. `rewriteStepTemplates` leaves any out-of-range or self-referencing index as an unresolved placeholder (rather than wiring a dead/self edge), because a replan frequently emits a stale `depends_on:[0]` pointing at a prior-frame node this plan can't reach.

Validation rules:

- **Direct params** — a key is allowed if it appears in `tool.Parameters().properties` OR the schema's `additionalProperties` is true (default when absent, per JSON Schema). Tools with `additionalProperties: false` get strict checking; tools like `compute` that legitimately accept arbitrary param names (typically wired in via `${step.N.field}` placeholders) stay loose.
- **Templates** — `${step.N}` (no field) substitutes the whole upstream result. `${step.N.path.to.field}` extracts via `extractJSONFieldAny` (preserves type when the placeholder is the entire string value, stringifies when embedded mid-string).

Both direct-param rejection and template failures emit `[dispatch:reject]` log lines so telemetry can count hallucination rate without scraping.

## Budgets

Every LLM-bearing component decrements a counter in `Budget`. `MaxLLMCalls` is the global cap; `maxReplans` and `maxInvestigations` cap replan and debug rounds (the replan cap auto-scales with plan difficulty). Exhausting the budget before:

- Reflector → a canned "budget exhausted" summary is written; no more batches.
- Holmes → the investigation is marked halted with a provisional hypothesis (`SuggestedStrategy` says so) and hands off to the microplanner anyway.
- Aggregator → the last reflector `verdict` is surfaced verbatim (the aggregator itself is exempt and still runs when reached).

`MaxNodes` caps the graph size. When a graft would push past it, the scheduler logs "budget exhausted, truncating plan" and skips the graft — partial completion over runaway spawn.

## Run cancellation & lifecycle

`RunDAGSync` wraps the whole DAG in `dagCtx, cancel := context.WithCancel(ctx)` — a DAG-wide cancel that is a **child of the job context** the scheduler minted for this run. There is no hard DAG wall clock; that nesting is what makes Stop clean:

- **Stop** (the button) — `API.handleStop → Agent.Cancel → Kernel.Cancel → Scheduler.Cancel → job.cancel()` cancels the job context, the parent of `dagCtx`, so the whole DAG unwinds: the scheduler loop drops through `<-ctx.Done()`, inflight nodes are abandoned, and the **aggregator is skipped** (it bails on `dagCtx.Err()` before running). The run ends with no synthesized answer, by design.
- **Client disconnect is NOT a Stop.** It only stops the caller that was blocked in `SubmitSync` from waiting — the job keeps running to completion on its worker. Cancelling the job context (Stop) is the only thing that actually tears the run down.
- **A newer same-session message preempts.** A still-queued job for the session is **superseded** (chat = newest wins; its caller gets `ErrPreempted`). If a job for the session is already RUNNING, the new message is not a cancel — it is routed into the live run as an **interjection** on the per-query steering channel, so the running query is redirected rather than killed.

The priority queue, worker pool, preemption, stop/cancel, and interject machinery are documented in **[scheduling.md](scheduling.md)**.

## Relevant source

| file | responsibility |
|---|---|
| `internal/agent/dag.go` | Graph, Node, NodeType, NodeState, topological ordering, DAG modes |
| `internal/agent/scheduler.go` | batch execution, graft hooks (debug/Holmes/microplanner), budget, cascade prune |
| `internal/agent/dispatcher.go` | per-node execute: injection, throttle, gate, dispatch, audit |
| `internal/agent/dispatcher_validation.go` | `validateDirectParams`, `validateParamRef`, `parseToolSchema` |
| `internal/agent/preflight.go` | `routeQuery` (cheap route) + `classifyInvestigate` (plan-prep) |
| `internal/agent/executive.go` | native `plan()` planner + replan-frame re-plan + `planStepsToNodes` |
| `internal/agent/reflection.go` | between-batch classifier: continue / replan / conclude |
| `internal/agent/builtin_debug.go` | `debug` super-tool — the door that grafts Holmes |
| `internal/agent/rca.go` | Holmes ReAct root-cause investigator + `spawnFirstHolmes` + RCAReport |
| `internal/agent/microplanner.go` | clean-room debugger — RCA → fix plan |
| `internal/agent/aggregator.go` | final answer synthesis + `decideAutoAggMode` |
| `internal/agent/edge_coverage.go` | coverage edge — frames empty/failed gathers so the aggregator acknowledges absence |
| `internal/agent/compute.go` | runCompute, computePlan, computeCode |
| `internal/agent/builtin_compute.go` | ComputeTool schema + dispatch wrapper |
| `internal/agent/builtin_edit_file.go` | EditFileTool — task_files-required wrapper over the coder |
| `internal/agent/contextgate.go` | source selection for LLM prompt assembly |
| `internal/agent/prompt/prompts.md` | ROUTE / PREFLIGHT / REFLECTOR / HOLMES / MICROPLANNER / AGGREGATOR prompts |
</content>
</invoke>
