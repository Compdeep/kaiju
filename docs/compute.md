# Compute Node

The compute node handles software development, code generation, and complex computations that don't fit into single-tool operations. It separates **planning** (architect decides *what*) from **implementation** (coders write *how*).

## Architecture

```
Planner emits:        compute(deep, goal="build a webapp", mode="deep")
                          │
                          ▼
Scheduler:            creates NodeCompute, dispatches via fireNode → dispatcher
                          │
                          ▼
Dispatcher:           type-asserts ContextualExecutor, builds ExecuteContext
                          │
                          ▼
ComputeTool:          .ExecuteWithContext(ec, params) → a.runCompute(ec, params)
                          │
                          ▼
runCompute:           if mode=deep && no blueprint → computePlan (architect)
                      if mode=deep && blueprint    → computeCode (coder)
                      if mode=shallow              → computeCode (single pass)
                          │
                          ▼
computePlan:          architect LLM call
                      returns {plan, interfaces, schema, setup[], tasks[], validation[]}
                          │
                          ▼
Scheduler graft:      4 phases, all children of the compute plan node
                      ├── Phase 1: setup bash nodes (sequential)
                      ├── Phase 2: coder nodes (parallel, one per task)
                      ├── Phase 3: execute/service grafts (from architect task params, per-coder)
                      └── Phase 4: validation nodes (parallel, depend on phases 1-3)
                          │
                          ▼
Each coder:           runs as a NodeCompute in shallow mode
                      receives architect's brief, interfaces, schema, task_files
                      returns {files_created, code_path, execute?, service?}
                          │
                          ▼
Reflector:            sees structured pass/fail from validation wave
                      concludes with confidence or replans targeting failures
```

## Data flow — complete pipeline trace

This is the full end-to-end data flow for a compute(deep) request like
"build a webapp with hero section and JWT auth." Every handoff is listed
with the source file and line range. Use this as the reference when
debugging why the architect, coder, or reflector made a bad decision —
trace backward from the decision to find which input was wrong or missing.

```
USER REQUEST
    │
    ▼
PREFLIGHT (scheduler.go:138)
    │  LLM classifies: mode=investigate, skills=[webdeveloper], intent=rank(100)
    │  Result stored in a.activeCards and a.preflight
    │
    ▼
PLANNER (scheduler.go:172)
    │  Sees skill guidance (planner.go:283 — extracted from webdeveloper ## Planning Guidance)
    │  Produces: compute(deep, goal="build a webapp...")
    │
    ▼
SCHEDULER creates NodeCompute, dispatches via fireNode
    │
    ▼
DISPATCHER (dispatcher.go:370)
    │  Detects ContextualExecutor interface
    │  Calls a.resolveComputeSkillCards() (exec_context.go:59)
    │    ├── Iterates a.activeCards (from preflight)
    │    ├── For each skill, extracts:
    │    │     ## Architect Guidance → architectParts
    │    │     ## RULES             → appended to architectParts
    │    │     ## Coder Guidance     → coderParts
    │    └── Returns map: {"architect": "...", "coder": "..."}
    │  Builds ExecuteContext with SkillCards populated
    │  Calls compute.ExecuteWithContext(ec, params)
    │
    ▼
COMPUTE runCompute (compute.go:82)
    │  Extracts: architectGuidance = ec.SkillCards["architect"]
    │            coderGuidance     = ec.SkillCards["coder"]
    │  mode=deep, no planRef → calls computePlan()
    │
    ▼
ARCHITECT — computePlan (compute.go:269)
    │
    │  READS:
    │    interfaces.json[sessionId]     → "## Existing Interfaces" in user prompt
    │    blueprints/*.blueprint.md      → "## Existing Blueprints" in user prompt
    │    workspace file tree            → "## Existing Codebase" in user prompt
    │    worklog                        → "## Recent Work Log" in user prompt
    │
    │  SYSTEM PROMPT:
    │    baseComputeArchitectPrompt (prompts.go:472)
    │      + "## Domain Guidance\n" + architectGuidance (from webdeveloper skill)
    │    Contains: database default, paths rule, blueprint format spec, task rules
    │
    │  LLM CALL → architect returns JSON:
    │    {
    │      "plan": "# Full Blueprint Markdown\n## Architecture\n...",
    │      "interfaces": { ... },
    │      "schema": { ... },
    │      "setup": ["mkdir...", "npx create-next-app..."],
    │      "tasks": [{ goal, task_files, brief, execute, service, depends_on_tasks }],
    │      "validation": [{ name, check, expect }]
    │    }
    │
    │  WRITES:
    │    blueprints/<tag>.blueprint.md  ← the "plan" field (full markdown)
    │    blueprints/interfaces.json     ← merged interfaces + schema for this session
    │
    │  CREATES follow_up tasks, each with:
    │    plan_ref → path to blueprint file
    │    brief, task_files, interfaces, execute, service
    │
    ▼
SCHEDULER GRAFT (scheduler.go:613)
    │
    │  Phase 1: Setup bash nodes (sequential)
    │  Phase 2: Coder compute(shallow) nodes (parallel)
    │  Phase 3: Execute/service nodes (depend on ALL coders)
    │  Phase 4: Validator bash nodes (depend on all above)
    │  Stores validators on graph.Validators for replay after replans
    │
    ▼
CODER — computeCode (compute.go:461)   ← runs for EACH task
    │
    │  READS:
    │    blueprint file from plan_ref   → "## Blueprint (authoritative)" in user prompt
    │
    │  USER PROMPT:
    │    ## Goal — task-specific goal
    │    ## Blueprint (authoritative — follow this exactly) — full blueprint markdown
    │    ## Interfaces (implement exactly to spec) — from task params
    │    ## Architect Brief — from task brief field
    │    ## Your Task Files (write ONLY these) — from task_files
    │    ## Project Structure — workspace tree
    │    ## Recent Work Log — what other coders have done
    │
    │  SYSTEM PROMPT:
    │    baseComputeCoderPrompt (prompts.go:578)
    │      + "## Domain Guidance\n" + coderGuidance (from webdeveloper ## Coder Guidance)
    │    Contains: paths rule, output format, coding guidelines
    │
    │  LLM CALL → coder returns JSON: { language, filename, code }
    │
    │  WRITES:
    │    workspace/project/<task_file>  ← the generated code
    │    Enforces project/ prefix on all paths
    │    Enforces project/ prefix on execute commands
    │
    ▼
PHASE 3 — execute/service nodes fire after ALL coders complete
    │  bash: "cd project/frontend && npm install && npm run build"
    │  service: "node project/backend/src/server.js"
    │
    ▼
PHASE 4 — validators fire after Phase 3
    │  curl -sf http://localhost:4000/health
    │  cd project/frontend && npm run build
    │  etc.
    │
    ▼
REFLECTOR (reflection.go:129)
    │  READS:
    │    loadLatestBlueprint(workspace) → "## Blueprint (the design this work should match)"
    │    All node results as evidence
    │  DECIDES: continue / conclude / replan
    │  If replan: re-grafts stored validators after new nodes
    │
    ▼
AGGREGATOR
    │  Synthesizes all evidence into user-facing response
    │  If services are live, includes link: "Your site is live at http://..."
    │
    ▼
MULTI-TURN (next user message in same session)
    │  Architect reads:
    │    blueprints/interfaces.json[sessionId] — existing API contracts
    │    blueprints/<tag>.blueprint.md — existing blueprint
    │  Extends rather than rewrites
```

### Key files

| File | Role |
|------|------|
| `internal/agent/compute.go` | runCompute, computePlan (architect), computeCode (coder), interfaces I/O, blueprint I/O |
| `internal/agent/prompts.go` | baseComputeArchitectPrompt, baseComputeCoderPrompt, prompt builders |
| `internal/agent/exec_context.go` | resolveComputeSkillCards — extracts ## Architect Guidance + ## RULES + ## Coder Guidance |
| `internal/agent/scheduler.go` | 4-phase graft, retry proxy, re-validation after replan |
| `internal/agent/reflection.go` | loadLatestBlueprint injection into reflector context |
| `internal/agent/dispatcher.go` | ContextualExecutor detection, ExecuteContext construction |
| `skills/bundled/webdeveloper/SKILL.md` | Domain skill with ## Planning Guidance, ## Architect Guidance, ## RULES, ## Coder Guidance |
| `workspace/blueprints/*.blueprint.md` | Blueprint files (the design documents) |
| `workspace/blueprints/interfaces.json` | Shared interfaces keyed by session ID |

### Debugging checklist

If the architect makes bad decisions:
1. Check `/tmp/kaiju_architect_system.txt` — does it contain the skill's RULES section?
2. Check `/tmp/kaiju_architect_user.txt` — does it contain existing interfaces and blueprints?
3. Check the blueprint file — is it detailed or a one-liner?

If the coder makes bad decisions:
1. Check `/tmp/kaiju_coder_system.txt` — does it contain coder guidance from the skill?
2. Check `/tmp/kaiju_coder_user.txt` — does it contain the blueprint? The interfaces? The task_files?
3. Check if task_files have the project/ prefix

If validators fail:
1. Are services actually running? Check `.services.json`
2. Are paths consistent between coders and validators? All should use project/
3. Did the execute commands install dependencies before building?

If multi-turn breaks:
1. Check `blueprints/interfaces.json` — does the session have the right entries?
2. Check if old blueprint exists in `blueprints/` — architect should extend it

## Tool shape

Compute is registered as `ComputeTool` in `internal/agent/builtin_compute.go`. It implements both the standard `Tool` interface (for registry/schema visibility to the planner) and the optional `ContextualExecutor` interface (for the real execution path that needs live graph/budget/LLM references).

The plain `Execute(ctx, params)` method is a defensive stub that returns an error — compute is always invoked via `ExecuteWithContext`.

## Two modes

### Shallow
One LLM call via `computeCode`. Used when the task is self-contained: a single file, a one-off script, a focused computation. The coder receives the goal, any architect context, and writes the code directly.

Output shape:
```json
{
  "type": "result",
  "files_created": [...],
  "code_path": "/tmp/...",
  "language": "python",
  "execute": "...",
  "service": {...}
}
```

### Deep
Two-phase. First `computePlan` runs the architect LLM call that decomposes the work into tasks. The scheduler then grafts one shallow compute node per task, running them in parallel. Each shallow node has the architect's brief and interfaces, so output stays consistent across parallel coders.

Output shape (from the architect):
```json
{
  "type": "plan",
  "plan_ref": "/path/to/blueprint.md",
  "plan": "full blueprint markdown (see architect prompt for format)",
  "setup": ["shell command", ...],
  "follow_up": [{"tool":"compute", "params":{...}}, ...],
  "validation": [{"name":"...", "check":"...", "expect":"..."}, ...]
}
```

The `follow_up`, `setup`, and `validation` fields drive the scheduler's 4-phase graft.

## Scheduler graft (4 phases)

When a deep compute node resolves with `type: "plan"`, the scheduler grafts the returned work as descendants:

### Phase 1 — Setup
Sequential bash nodes from `setup[]`. Used for scaffolding (`npm create vite`), dependency installation (`npm install`, `pip install`), directory creation, initial DB setup. Each setup command depends on the previous one.

### Phase 2 — Coder tasks
Parallel NodeCompute nodes from `follow_up[]`, each in shallow mode with the architect's brief/interfaces/schema. Each task writes exactly one file. Inter-task dependencies are honored via `depends_on_tasks` indices.

### Phase 3 — Execute / Service
The architect's tasks carry optional `execute` (one-shot bash command) and
`service` (long-running process) fields. The scheduler grafts these at plan
time, each depending on its coder node. This is where `npm run build`,
`node db/seed.js`, and backend servers get started — AFTER the code is
written but BEFORE validation runs. If a coder also returns `execute`/`service`
in its result, the scheduler grafts those as additional bonus nodes.

### Phase 4 — Validation
After all Phase 1-3 work completes, parallel bash nodes run each check from
the architect's `validation[]` array. Validators depend on ALL prior phases
(setup + coders + execute/service), so they only fire after servers are
actually running. These prove the goal was actually achieved (HTTP 200, query
succeeds, test passes), not just that files were written. Tagged
`verify_<name>` for UI visibility. Timeout 15s per check.

Validation failures flow back to the reflector as structured evidence. The
reflector either replans to fix or concludes with the failure reported.

## Failure handling

The DAG uses **optimistic scheduling**: all nodes fire as soon as their
dependencies resolve, and failures accumulate as signals for the reflector
and aggregator.

### Micro-planner retries
When a tool node fails, the micro-planner (a lightweight LLM call) examines
the error and creates replacement nodes with a different approach. It receives
all previous failures for that node so it doesn't repeat the same mistake.

### Retry proxy pattern
Nodes in a dependency chain use a stricter model: if a setup node fails and
other nodes depend on it, the original node stays "running" (dependents remain
blocked) while the micro-planner retries. The replacement's result is copied
back into the original when it succeeds. After `max_node_retries` (default 2)
failures, the original resolves with the error and dependents unblock in
degraded state.

Nodes with no dependents skip the proxy pattern — they fail normally and
their error flows to the reflector as evidence. This avoids wasting retry
budget on leaf nodes where ordering doesn't matter.

## Blueprints and Interfaces

### Blueprints
Full markdown design documents at `<workspace>/blueprints/<tag>.blueprint.md`. Written by the architect, read by coders and reflectors as the single source of truth. Contains: architecture, directory structure, file manifest with per-file documentation, conventions, interfaces, schema, setup rationale, and validation criteria.

On multi-turn conversations, the architect reads existing blueprints and extends them rather than starting fresh.

### Interfaces
Shared API contracts at `<workspace>/blueprints/interfaces.json`, keyed by session ID:

```json
{
  "session-abc-123": {
    "interfaces": {
      "POST /api/auth/login": {"request": {"email": "string"}, "response": {"token": "string"}}
    },
    "schema": {
      "type": "sqlite",
      "tables": {"users": "id integer primary key, email text unique, ..."}
    }
  }
}
```

The architect reads interfaces at the start of each run (shown as `## Existing Interfaces (authoritative)`), and merges new interfaces/schema back on completion. Additive merge: new keys added, existing keys preserved unless explicitly superseded. Persists across turns in the same conversation.

## Skill injection

When the preflight call picks skills (via classifier selection), compute's prompts are enriched with `## Architect Guidance` and `## Coder Guidance` sections extracted from the selected skill cards. This is how the `webdeveloper` skill teaches the architect to decompose webapps into ~15-25 files and teaches coders what real production components look like.

Skill resolution happens in the dispatcher's `resolveComputeSkillCards()` based on `a.activeCards`. The resolved sections flow through `ec.SkillCards` into the architect and coder prompts. Multiple active skills concatenate with per-skill headers.

Node attribution: when a compute node runs with active skills, `n.Skills` is populated and the frontend renders a `guided by [skill_name]` sub-row under the node in the trace view.

## Prompts

Base prompts live in `internal/agent/prompts.go`:

- `baseComputeArchitectPrompt` — general architect principles (decomposition, interfaces, schema, tasks, validation)
- `baseComputeCoderPrompt` — general coder principles (clean complete code, build complete solutions, deep modules, no TODOs)

Builder functions `buildComputeArchitectPrompt(guidance)` and `buildComputeCoderPrompt(guidance)` append skill-card guidance sections when present. Empty guidance returns the base prompt unchanged.

The architect prompt commits the architect to emit `validation[]` ("A build with no verification is not a complete plan"), to respect existing contracts ("Your new interfaces/schema must stay consistent — add keys freely, but never silently rename existing ones"), and to preserve existing functionality when modifying existing code ("your task is additive or targeted, not a rewrite").

## Token budgets

- Architect (`computePlan`): 8192 tokens — enough to decompose into many tasks with interfaces and schema
- Coder (`computeCode`): 16384 tokens — enough to write real files with full error handling, not skeletons

These are upper bounds, not targets. Small tasks still produce small outputs; large tasks no longer get truncated mid-function.

## Artifacts

Blueprints: `<workspace>/blueprints/<tag>.blueprint.md`
Interfaces: `<workspace>/blueprints/interfaces.json`
Per-task generated code: `/tmp/kaiju-compute-<tag>-XXXXX/...` (cleaned up after the node completes)
Service logs (from Phase 3 service grafts): `<workspace>/.services/<name>.{out,err}.log`

## DAG integration

Compute is a `NodeCompute` in the DAG (distinct from `NodeTool` only for graft classification). Fires through `fireNode` → dispatcher → `ExecuteWithContext`. All standard DAG features apply:

- **param_refs** — inject upstream results into compute params before execution
- **depends_on** — standard dependency ordering across the graph
- **Reflection** — reflector evaluates compute results as evidence; can replan on failure
- **Budget** — each compute node costs 1 node + 1 LLM call; deep mode costs 1 plan + N coder calls
- **Microplanner** — failed coder nodes can be repaired with hints

## When to use compute vs other tools

| Task | Tool |
|------|------|
| `ls`, `grep`, `git status`, `curl` | bash |
| `npm install`, `pip install` | bash |
| Start a server, daemon | service |
| Single-file Python script | compute (shallow) |
| Data analysis pipeline with interfaces | compute (deep) |
| Build a webapp, full-stack project | compute (deep) |
| Scaffold a new project | compute (deep) |
| Multi-file refactor | compute (deep) |

## Configuration

```json
{
  "tools": {
    "compute": {
      "enabled": true,
      "timeout_sec": 120
    }
  }
}
```

## Related

- Skill system — see `docs/architecture.md` and `skills/bundled/*/SKILL.md`
- Validation wave details — see the Phase 4 section above
- Session contract — per-conversation state for drift prevention
- Service tool — see `docs/service.md`
- Intent-based execution — see `docs/authorization.md`
