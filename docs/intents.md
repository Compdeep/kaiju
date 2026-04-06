# Intents

Intents are the IBE (Intent-Based Execution) safety levels. Kaiju starts with three built-ins and lets admins add more.

## Built-in intents

| Name | Rank | Description |
|------|------|-------------|
| `observe` | 0 | Read-only — inspect data and state without making changes |
| `operate` | 100 | Normal work — reversible side effects |
| `override` | 200 | Destructive — irreversible actions |

Ranks are **sparse integers**. Built-in ranks leave room for custom intents to be inserted between them (e.g. `triage` at 50, `kill` at 300).

## Custom intents

Admins can add intents via the frontend (`Admin → Intents tab`) or directly via the REST API. Each intent has:

- **name** — unique identifier, used by the planner and returned by preflight
- **rank** — integer for ordering; higher ranks require higher request intents
- **description** — short UI label shown in tooltips and admin lists
- **prompt_description** — text shown to the LLM in planner and preflight prompts
- **is_builtin** — true for observe/operate/override, cannot be changed or deleted

Example custom intents:

```json
[
  {"name": "triage", "rank": 50, "description": "Diagnostic read + inspect"},
  {"name": "kill", "rank": 300, "description": "Process termination, emergency cleanup"}
]
```

These show up in:
- The chat intent dropdown (fetched from `/api/v1/intents`)
- The preflight classifier's output schema (as valid enum values)
- The planner's IBE prompt section (descriptions injected dynamically)

## Tool intent assignment

Every tool has a default impact declared in its Go `Impact(params)` method. The intent registry can override any tool's impact without touching code:

1. Admin → Intents tab → Tool Assignments section
2. Pick a tool from the list
3. Dropdown shows all available intents
4. Select the intent the tool should now use
5. Restart kaiju to apply

Example: admin wants `bash` to require `override` intent (not `operate` as the Go default). They set `bash → override` in the admin UI. After restart, the gate rejects any bash call at operate intent.

Removing an override restores the Go default.

## How resolution works

When a tool is about to execute:

```
dispatcher.executeToolNode
  ↓
impact := a.intentRegistry.ResolveToolIntent(toolName, tool, params)
  ↓
compiled := tool.Impact(params)       // returns a rank (0, 100, or 200)
if DB assignment exists for toolName → return min(compiled, assignment's rank)
else → return compiled
```

The DB assignment acts as a **ceiling** — it can lower a tool's effective impact
but never raise it. Per-invocation granularity from `tool.Impact(params)` is
always preserved. For example, `bash("ls")` returns rank 0 regardless of
whether bash is assigned to a high intent.

The resolved rank is what the IBE gate compares against the request intent, scope caps, and clearance.

## How prompts see intents

Both preflight and planner pull intent descriptions from the registry at call time:

- **Preflight prompt**: the `intent` field in its output schema lists all registered intent names as an enum, and the description section under `**intent**` is generated from `registry.PromptBlock()`.
- **Planner prompt (structured mode)**: the `## Intent-Based Execution` section is generated from the registry. Shows allowed levels (filtered by scope cap) and the gate enforcement rule.

Adding a custom intent and restarting kaiju makes it immediately visible to the LLM. No prompt edits required.

## Database schema

Two tables in `kaiju.db`:

```sql
CREATE TABLE intents (
  name               TEXT PRIMARY KEY,
  rank               INTEGER NOT NULL,
  description        TEXT NOT NULL DEFAULT '',
  prompt_description TEXT NOT NULL DEFAULT '',
  is_builtin         INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE tool_intents (
  tool_name   TEXT PRIMARY KEY,
  intent_name TEXT NOT NULL
);
```

Seeded with observe/operate/override on first run. User-added intents are `is_builtin=0`.

## API

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/v1/intents` | List all intents |
| POST | `/api/v1/intents` | Create a custom intent |
| PUT | `/api/v1/intents/{name}` | Update description/rank (builtins can only change descriptions) |
| DELETE | `/api/v1/intents/{name}` | Delete a custom intent (builtins rejected) |
| GET | `/api/v1/tool-intents` | List tools with current + default intent assignments |
| PUT | `/api/v1/tool-intents/{tool}` | Set override |
| DELETE | `/api/v1/tool-intents/{tool}` | Remove override (fall back to Go default) |

Deletion of a custom intent cascades to `tool_intents` — any tools pointing at the deleted intent automatically lose their override.

## Restart required

Intent changes do NOT hot-reload. The `IntentRegistry` is loaded once at agent startup from the DB. Admins must restart kaiju after making changes. The admin UI shows a note reminding them.

Rationale: hot-reload adds complexity for a setting that changes rarely. Restart is explicit and safe.

## Relationship to scopes

Intents and scopes are **different concepts**:

- **Intent** = what THIS request is allowed to do. Set per-request by the user ("this chat is observe-only"). A ceiling.
- **Scope** = what THIS USER is allowed to do EVER. Set by admin, attached to users. A whitelist of tools + per-tool caps.

Both stack at the gate: `tool_impact ≤ min(request_intent, user.max_intent, scope.MaxImpact[toolName], clearance)`. See `docs/authorization.md` for the full triad.

Scopes reference intents by name. When you add a custom `triage` intent, scopes that cap at `triage` reference the new name. Scopes become more expressive as you add intents.

## Constraints

- **Rank ordering is linear** — the gate compares ranks as a single scalar. Non-linear permission policies (e.g. "can kill processes but can't write files, both are 'destructive'") belong in scopes, not intents.
- **Intent names are a protocol** — the string names are parsed by the gate, API, planner output, and frontend dropdown. Renaming built-in names breaks everything. Custom names are free-form.
- **Tools declare a default via Go `Impact()`** — the registry override supersedes, but the Go default is the fallback. Tools can still vary impact by params (e.g. bash could inspect its command), though none currently do.

## Example admin workflow

Scenario: admin wants a new "read_all" intent that allows broader read access than `observe`, and assigns the `compute` tool to it.

1. Admin → Intents tab → `+ new`
2. Fill in: name=`read_all`, rank=`20`, description="Broad read access including filesystem scans", prompt_description="Read-only investigation tasks that may touch many files or databases"
3. Save. Kaiju warns: restart required.
4. Admin → Tool Assignments section
5. Find `compute` row → dropdown → select `read_all`
6. Save.
7. Restart kaiju.
8. On next investigation, compute gates at `read_all` (rank 20). A request at `observe` (0) will block compute. A request at `operate` (100) allows it.

## Implementation

- `internal/db/intents.go` — DB types, CRUD, seeding
- `internal/agent/intent_registry.go` — in-memory registry, Load/ResolveToolIntent/PromptBlock
- `internal/api/intent_handlers.go` — REST handlers
- `web/src/components/tabs/IntentsTab.vue` — admin UI
- Preflight and planner both pull from `a.intentRegistry` at prompt-building time

Full test coverage: `internal/db/intents_test.go`, `internal/agent/intent_registry_test.go`, `internal/agent/intent_enforcement_test.go`.
