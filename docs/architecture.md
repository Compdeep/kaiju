# Kaiju Architecture

Kaiju is a general-purpose AI assistant built in Go. It combines a battle-tested DAG-based agent engine with a modular channel system, REST API, and pluggable tool registry.

## High-Level Overview

```
┌──────────────────────────────────────────────────────────┐
│                        cmd/kaiju                         │
│              chat │ serve │ run "query"                   │
└──────────┬───────────────┬───────────────────────────────┘
           │               │
    ┌──────▼──────┐  ┌─────▼──────┐
    │  Channels   │  │  REST API  │
    │ cli│web│tg  │  │ /execute   │
    └──────┬──────┘  └─────┬──────┘
           │               │
     ┌─────▼───────────────▼─────┐
     │        Agent Engine       │
     │  Planner → DAG Scheduler  │
     │  → Reflection → Aggregator│
     └─────────────┬─────────────┘
                   │
     ┌─────────────▼─────────────┐
     │       Tool Registry       │
     │  bash│file│web│sysinfo│…  │
     └─────────────┬─────────────┘
                   │
     ┌─────────────▼─────────────┐
     │    IBE Gate (Safety)      │
     │ observe│operate│override  │
     └──────────────────────────-┘
```

## Core Packages

| Package | Purpose |
|---------|---------|
| `cmd/kaiju` | Entry point — CLI, daemon, one-shot modes |
| `internal/agent` | DAG agent engine — planner, preflight, scheduler, reflection, compute, contextual executor |
| `internal/agent/llm` | OpenAI/Anthropic-compatible HTTP client |
| `internal/agent/tools` | Tool interface + thread-safe registry |
| `internal/agent/gates` | Intent-Based Execution (IBE) safety gate |
| `internal/agent/skillmd` | SKILL.md hot-reload loader for user-defined guidance skills |
| `internal/db` | SQLite persistence for users, scopes, intents, sessions, memories, audit |
| `internal/channels` | Channel plugin interface + registry |
| `internal/gateway` | HTTP server, WebSocket, SSE streaming |
| `internal/api` | REST execution API |
| `internal/tools` | General-purpose tools (bash, file, web, sysinfo, memory) |
| `internal/config` | JSON config loader with env var expansion |
| `internal/compat` | Shim layer for omamori dependencies (store, protocol, ipc) |
| `pkg/gossip` | Optional P2P mesh networking module |
| `pkg/sidecar` | Optional IPC sidecar protocol for external integrations |

## Agent Execution Flow

1. **Trigger** arrives (chat query, API call, or channel message)
2. **Preflight** (executor LLM call) classifies the query in one shot — returns `{skills, mode, intent, required_categories}`. If `mode=chat` or `mode=meta`, short-circuits past the planner. Otherwise its classification flows into the planner's prompt as context.
3. **Planner** LLM call generates a DAG of tool invocations with dependencies. Sees preflight-selected skills via `a.activeCards`, scope-filtered tools, and configurable intent descriptions from the registry.
4. **Scheduler** executes the DAG with mode-specific behavior:
   - `reflect`: serialized with reflection barriers between depth waves
   - `nReflect`: parallel with batched reflection every N completions
   - `orchestrator`: parallel with per-node observer LLM calls
5. **Reflection** checkpoints decide: continue, conclude early, or replan. Reflector requires direct evidence of goal achievement to conclude — writing files is not achievement, only verified working behavior counts.
6. **Micro-planner** handles individual node failures (skip, retry, or replace)
7. **Aggregator** synthesizes all tool results into a final verdict
8. **Actuator** executes any follow-up actions (gated by IBE)

## Preflight

Single executor-model LLM call at investigation start that replaces the legacy classifier. Returns a structured `PreflightResult` with four fields:

- **skills**: names of guidance skills the query matches (from classifier manifest)
- **mode**: `chat | meta | investigate` — chat and meta short-circuit past the planner
- **intent**: `observe | operate | override | <custom>` — the safety level this query needs, resolved via the intent registry
- **required_categories**: tool categories the plan must include (`network`, `filesystem`, `compute`, `process`, `info`)

Preflight sees conversation history (last 5 turns, assistant replies truncated to 500 chars) so it can resolve short follow-ups like "yeah do it." Its output is cached on `a.preflight` for the duration of the investigation.

## Skills and capabilities

Kaiju has two distinct concepts with related names:

- **Tools** — compiled Go code that implements the `Tool` interface. They have an `Execute()` method that actually does work. Examples: `bash`, `file_read`, `compute`, `web_search`, `service`.
- **Skills** — markdown files that provide planning guidance via prompt injection. They never execute. Two flavors:
  - **CapabilityCards** (`internal/agent/prompts/capabilities/*.md`, embedded in binary) — small domain buckets (`data_retrieval`, `system_operations`, etc.)
  - **SkillMD** (`skills/bundled/<name>/SKILL.md`, loaded from disk) — user-editable, full-featured guidance cards with `## When to Use`, `## Planning Guidance`, `## Architect Guidance`, `## Coder Guidance` sections

Both types are selected per-query by the preflight call and injected into relevant prompts:

- **Planner** reads `## Planning Guidance` from active skills
- **Architect** (inside compute) reads `## Architect Guidance` from active skills
- **Coder** (inside compute) reads `## Coder Guidance` from active skills
- **Aggregator** reads `## Aggregator Guidance` from active capability cards

Guidance-only skills are stored in `a.skillGuidance` — separate from the tool registry to avoid confusing executable tools with prompt guidance.

## Compute and the validation wave

For code-generation and multi-file work, kaiju uses the `compute` tool which runs a four-phase pipeline: architect decomposes → setup commands → parallel coders → execute/service → validation wave. See `docs/compute.md` for details.

Failed nodes in a dependency chain use a **retry proxy pattern**: the original node stays "running" while the micro-planner retries, blocking dependents until a replacement succeeds or retries exhaust. Leaf nodes (no dependents) fail optimistically — their errors flow as signals to the reflector. This keeps the pipeline sequentially correct where ordering matters while staying optimistic everywhere else.

## Intent system

Intent levels (`observe`/`operate`/`override`) are stored in the DB and are configurable. Admins can add custom intents (e.g. `triage`, `kill`) with sparse integer ranks. Each tool's intent can be overridden per-tool via the admin UI. See `docs/intents.md` for the full intent system and `docs/authorization.md` for gate enforcement.

## Session contract

Each conversation has shared interfaces at `<workspace>/blueprints/interfaces.json` (keyed by session ID) holding cumulative API contracts and schema. The architect reads them as authoritative current state on every turn and merges new values back on completion. Blueprints (`<workspace>/blueprints/<tag>.blueprint.md`) are full markdown design documents that coders and reflectors read as the single source of truth.

## Budget & Resource Limits

The DAG engine enforces resource limits at two levels:

**Plan time** (planner creates nodes):
- `max_nodes` — hard cap on total nodes in the investigation
- Per-skill wave limits are NOT enforced at plan time — they apply at execution time only

**Execution time** (scheduler fires nodes):
- `max_per_skill` — per-skill wave limit (resets at reflection boundaries)
- `max_llm_calls` — total LLM calls (planner + reflections + aggregator)
- `wall_clock_sec` — investigation timeout

This separation matters: a plan like `web_search → 5 × web_fetch` is one logical chain.
The web_fetch nodes can't fire until their dependency completes, so they don't add parallel
pressure. Enforcing per-skill limits at plan time would truncate mid-chain, breaking
dependency injection (`param_refs`) and causing nodes to fire without their dependencies.

## Intent-Based Execution (IBE)

Every tool declares an impact rank. Every request carries an intent rank.
The gate enforces: `tool.Impact(params) ≤ min(intent, clearance, scope_cap)`.

Intent names and ranks are loaded from the config/DB via the intent registry.
Admins can add custom intents at any rank. Default ladder:

| Rank | Name      | Meaning                    | Examples |
|------|-----------|----------------------------|----------|
| 0    | observe   | Read-only                  | sysinfo, file_read, web_fetch |
| 100  | operate   | Reversible side effects    | file_write, bash (non-destructive) |
| 200  | override  | Destructive / irreversible | bash (rm, kill), system modification |

## Configuration

See `docs/config.md` for the full config reference.

## Optional Modules

- **Gossip mesh** (`pkg/gossip`): P2P agent coordination via libp2p. See `docs/gossip.md`.
- **Sidecar IPC** (`pkg/sidecar`): External process integration via NDJSON pipes. See `docs/sidecar.md`.
