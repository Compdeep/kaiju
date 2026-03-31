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
     │  tell │ triage │ act      │
     └──────────────────────────-┘
```

## Core Packages

| Package | Purpose |
|---------|---------|
| `cmd/kaiju` | Entry point — CLI, daemon, one-shot modes |
| `internal/agent` | DAG agent engine (copied from omamori, logic untouched) |
| `internal/agent/llm` | OpenAI/Anthropic-compatible HTTP client |
| `internal/agent/tools` | Tool interface + thread-safe registry |
| `internal/agent/gates` | Intent-Based Execution (IBE) safety gate |
| `internal/agent/skillmd` | SKILL.md hot-reload loader for user-defined skills |
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
2. **Classifier** (optional) selects capability cards for the query domain
3. **Planner** LLM call generates a DAG of tool invocations with dependencies
4. **Scheduler** executes the DAG with mode-specific behavior:
   - `reflect`: serialized with reflection barriers between depth waves
   - `nReflect`: parallel with batched reflection every N completions
   - `orchestrator`: parallel with per-node observer LLM calls
5. **Reflection** checkpoints decide: continue, conclude early, or replan
6. **Micro-planner** handles individual node failures (skip, retry, or replace)
7. **Aggregator** synthesizes all tool results into a final verdict
8. **Actuator** executes any follow-up actions (gated by IBE)

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

Every tool declares an impact level. Every request carries an intent level.
The gate enforces: `tool.Impact(params) ≤ min(intent, clearance)`.

| Level | Intent | Impact | Examples |
|-------|--------|--------|----------|
| 0 | observe | observe | sysinfo, file_read, web_fetch |
| 1 | operate | affect | file_write, bash (non-destructive) |
| 2 | override | control | bash (rm, kill), system modification |

## Configuration

See `docs/config.md` for the full config reference.

## Optional Modules

- **Gossip mesh** (`pkg/gossip`): P2P agent coordination via libp2p. See `docs/gossip.md`.
- **Sidecar IPC** (`pkg/sidecar`): External process integration via NDJSON pipes. See `docs/sidecar.md`.
