# Skills

Kaiju has two things that look similar but serve different purposes, and they're both called "skills" in different places. Getting the distinction right matters.

## Tools vs skills

- **Tools** — Go code that implements the `Tool` interface. Has an `Execute()` method that actually does work. Lives in `internal/tools/` or `internal/agent/` depending on whether it needs contextual execution. Examples: `bash`, `file_read`, `file_write`, `web_search`, `compute`, `service`.
- **Skills** — markdown files that provide planning guidance via prompt injection. Never execute. Their body gets read and pasted into LLM prompts at specific points.

Both can show up in the same lists (the registry, the UI trace rows), which caused confusion historically. In current code they're cleanly separated: tools live in `a.registry`, guidance-only skills live in `a.skillGuidance`.

## Two kinds of skills

### CapabilityCards
- Small, embedded in the binary at `internal/agent/prompts/capabilities/*.md`
- Four of them: `data_retrieval`, `general_reasoning`, `self_awareness`, `system_operations`
- Not user-editable without recompiling
- Good for stable, orthogonal "what kind of task is this" classification

### SkillMD
- Loaded from disk at `skills/bundled/<name>/SKILL.md` or `<workspace>/skills/<name>/SKILL.md`
- User-editable and extensible
- Hot-reloaded via file watcher
- Two sub-flavors based on frontmatter:
  - **Command-dispatch skills** (`command_dispatch: <tool_name>`) — wrap an existing compiled tool with added guidance. `Execute()` forwards to the wrapped tool.
  - **Guidance-only skills** (no `command_dispatch`) — prompt guidance only, no `Execute()`. Stored in `a.skillGuidance`, never in the tool registry.

## How skills are selected

Preflight (the executor-model LLM call at investigation start) returns a `skills` array — names of skills the query matches. These get stored in `a.activeCards` for the duration of the investigation.

The preflight manifest shows both capability cards and guidance SkillMDs with their descriptions, so the model can pick from either pool. Name collisions prefer capability cards.

## How guidance flows into prompts

Different downstream components consume different sections of the selected skills:

| Consumer | Section read |
|----------|--------------|
| Planner (structured mode) | `## Planning Guidance`, `## When to Use`, `## Approach Selection` |
| Aggregator | `## Aggregator Guidance` (from capability cards) |
| ReAct loop | full body of active capability cards |
| Compute architect | `## Architect Guidance` (from any active guidance skill) |
| Compute coder | `## Coder Guidance` (from any active guidance skill) |

Not every skill needs every section. The architect simply iterates active skills and includes whichever sections they contain.

## Writing a SkillMD

File: `skills/bundled/<name>/SKILL.md`

Frontmatter:
```yaml
---
name: webdeveloper
description: Full-stack web application development — frontend, backend, APIs, databases, auth, UI/UX
---
```

Required sections (pick what applies):
- `## When to Use` — classifier uses this to decide whether to activate the skill
- `## Planning Guidance` — read by planner when the skill is active
- `## Architect Guidance` — read by compute architect (for deep mode decomposition)
- `## Coder Guidance` — read by compute coder (quality bar for individual files)

Example sections included in kaiju:

- `skills/bundled/webdeveloper/SKILL.md` — decomposition for webapps, quality bar for UI components, auth patterns
- `skills/bundled/data_science/SKILL.md` — Python pipeline structure, pandas/sklearn conventions, verification patterns

Users can add their own by dropping a new directory under `workspace/skills/` or `<dataDir>/skills/`. Hot reload picks them up.

## Skill attribution in the trace

Compute nodes annotate themselves with active skills. When a compute node runs with `webdeveloper` active, the frontend DAG trace shows a "guided by [webdeveloper]" sub-row under the node. Mechanism:

- Dispatcher populates `n.Skills` from `a.activeCards` filtered to ones with architect/coder guidance sections
- `NodeInfo.Skills` serializes via SSE events
- `DAGTrace.vue` renders a sub-row with the skill name as a chip

## Classifier integration

The preflight classifier prompt sees a unioned manifest of both registries:

```
Available domains:
- data_retrieval: Telemetry queries, log analysis, system state retrieval
- general_reasoning: General analysis, conversation, questions
- system_operations: Process management, command execution, system admin
- self_awareness: Questions about agent capabilities and tools
- webdeveloper: Full-stack web application development...
- data_science: Python data analysis, statistics, ML workflows...
- kaiju_coder: Coding workflows — write, refactor, debug...
- playwright: Browser automation, scraping, MCP
- ...
```

Returns picks from either pool. Validation in the registry lookup handles both.

## Why the split exists

Capability cards are old — they were built before the SkillMD system. They're a lightweight "what kind of task" classifier with a fixed 4-bucket taxonomy. SkillMD is newer, richer, user-extensible, and designed for domain-specific expertise.

Merging them would require migrating the 4 capability cards to SkillMD format and updating 3+ consumer paths. Possible, not currently urgent. They coexist cleanly and the classifier sees both.

## Files

- `internal/agent/prompts.go` — CapabilityCard type, embedded loader, `ComposeBodies`, `ComposeAggregatorGuidance`
- `internal/agent/skillmd/*.go` — SkillMD parser, loader, watcher, frontmatter, gating
- `internal/agent/preflight.go` — manifest building, selection flow
- `internal/agent/dispatcher.go` — `resolveComputeSkillCards` (extracts architect/coder guidance for compute calls)

## Related docs

- `docs/compute.md` — how compute consumes architect/coder guidance
- `docs/architecture.md` — high-level placement in the agent flow
