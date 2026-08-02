# Kaiju Architecture

Kaiju is an **executive kernel** — a Go agent engine that runs system-administration
and enterprise workflows *alongside* the operating system, gating every action
through **Intent-Gate Execution**. It is a single compiled binary with a small
dependency surface; heavy capabilities are opt-in plugins kept out of the default
build.

> **Read the architecture overview:** a rendered, illustrated walkthrough of the
> whole system — life of a request, the DAG engine, IGX, plugins, routing, memory,
> lifecycle, topology — lives at **[`/docs/architecture`](architecture.html)**
> (served by kaiju on `:8090`).

This directory holds the detailed reference docs behind that overview.

## Engine
- **[graph.md](graph.md)** — the authoritative DAG engine: route → preflight →
  executive plan → scheduler → dispatcher → reflection → debug/Holmes → aggregator,
  the `${step.N}` edge wiring, the edges (coverage / grounding / conclusion-floor)
  anti-fabrication layer, compute, and run cancellation.
- **[scheduling.md](scheduling.md)** — the priority worker pool, node batches,
  preemption, stop/cancel, and interject.
- **[prompt-context.md](prompt-context.md)** — the ContextGate: the single context
  API and its sources, and the memory security boundary.

## Security
- **[authorization.md](authorization.md)** — the scope / intent / clearance triad
  and the gate that enforces `impact ≤ min(intent, clearance, scope)`.
- **[intents.md](intents.md)** — the configurable intent ladder, custom intents,
  and per-tool assignment.
- **[examples-igx.md](examples-igx.md)** — worked IGX scenarios.

## Tools & plugins
- **[tools.md](tools.md)** — the built-in tool catalogue, by impact tier.
- **[plugins.md](plugins.md)** — the dual plugin architecture (compiled in-process
  + the remote bridge to a supervised out-of-process host) and the service manager.
- **[uploads-extraction.md](uploads-extraction.md)** — office/PDF extraction, the
  uploads pipeline, and the `web_fetch` decoder / reader seams.
- **[actions.md](actions.md)** — node actions and frontend display hints.

## Memory, skills, models
- **[memory.md](memory.md)** — sessions, long-term memory, compaction, tenant
  isolation, and the chat-boundary rule.
- **[skills.md](skills.md)** — guidance skills vs capability cards, and how they are
  selected and injected.
- **[router-model-bench.md](router-model-bench.md)** — the model lanes and the
  benchmark behind the route-model default.

## Config & operations
- **[config.md](config.md)** — the full configuration reference.
- **[service.md](service.md)** — the `service` process-manager tool and its
  supervisor (health loop, auto-restart, one-instance guarantee).
- **[workspace.md](workspace.md)** — `data_dir` vs `workspace`, the sandbox, and
  bootstrap files.

## API
- **[api.md](api.md)** — the REST + SSE reference (execute, stop, interject,
  sessions, uploads, memories, clearance, …).

## Examples & positioning
- **[examples-office.md](examples-office.md)** — a narrative no-code walkthrough.
- **[datasheet.md](datasheet.md)** — the product one-pager.

## Design notes (history)
- **[design/](design/)** — shipped design docs kept for rationale
  (`replan-design.md`, `pre-plan-strategist-note.md`).
