# Authorization

Kaiju splits authorization into three independent dimensions. Each is checked at the gate before any tool executes. All three must pass.

## End-to-End Flow

How a user request becomes a gated tool execution:

```
User: "delete /tmp/test.txt"          ← human sets intent (e.g. "operate" = 100)
  │
  ▼
┌─────────────────────────────────────────────────────────────────────┐
│ PLANNER (LLM)                                                       │
│                                                                     │
│ Sees: tool list (filtered by scope), skill guidance, user query     │
│ Produces: [{"skill":"bash","params":{"command":"rm /tmp/test.txt"}}]│
│                                                                     │
│ The LLM imagines the command. It has no say in what happens next.   │
└──────────────────────────┬──────────────────────────────────────────┘
                           │  JSON plan (structured) or
                           │  tool_use response (native)
                           ▼
┌─────────────────────────────────────────────────────────────────────┐
│ SCHEDULER                                                           │
│                                                                     │
│ Creates DAG nodes from plan steps.                                  │
│ Fires nodes when dependencies resolve.                              │
│ Rejects hallucinated tool names (not in registry).                  │
└──────────────────────────┬──────────────────────────────────────────┘
                           │  node fires
                           ▼
┌─────────────────────────────────────────────────────────────────────┐
│ IBE GATE (Go code, not LLM)                                        │
│                                                                     │
│ Step 1 — SCOPE:     Is "bash" in user's allowed tools?              │
│                     Source: admin-defined scope in DB                │
│                                                                     │
│ Step 2 — IMPACT:    registry.ResolveToolIntent("bash", ...)          │
│                     Source: DB override (if admin configured) OR    │
│                     fallback to tool's Go Impact() method            │
│                     bash → rank 200 (override) via registry         │
│                                                                     │
│ Step 3 — INTENT:    impact(200) ≤ min(intent(100), scope_cap(100)) │
│                     Source: user's session setting (operate = 100)  │
│                     200 > 100 → BLOCKED                             │
│                                                                     │
│ Step 4 — CLEARANCE: (never reached — cheaper checks failed first)   │
│                     Source: external HTTP endpoint                   │
│                                                                     │
│ Result: "gate: bash blocked (impact=200 > ceiling=100)"             │
│ The rm command NEVER executes. Logged in audit trail.               │
└─────────────────────────────────────────────────────────────────────┘
```

Key principle: **the LLM produces the plan, but Go code decides what runs.** Impact is resolved by the intent registry (DB override with Go `Impact()` fallback), not by the LLM. Intent is set by the human operator, not by the LLM. The LLM is untrusted — it can plan anything, but the gate enforces reality.

Impact resolution is two-layered:
1. If an admin has set a DB override for this tool (via the Intents admin tab), the override's rank wins.
2. Otherwise, the tool's Go `Impact(params)` method runs. Compiled tools return ranks directly (0, 100, or 200) on the same scale as the intent registry. The gate compares impact and intent as plain integers — no translation step.

This means admins can reclassify any tool's impact without touching Go code — a `bash` that defaults to `override` can be pinned to `operate` for a particular deployment by setting the DB override. See `docs/intents.md` for the admin workflow.

This is identical for both planner modes (structured JSON and native function calling). The gate sees the same data either way: a tool name and parameters. It doesn't know or care how the plan was produced.

```
┌─────────────────────────────────────────────────────────┐
│                    Gate Check Order                       │
│                                                          │
│  1. Scope      →  is this tool visible to the user?      │
│  2. Intent     →  is the impact level allowed?            │
│  3. Clearance  →  does the external authority approve?    │
│                                                          │
│  All three pass  →  tool.Execute()                        │
│  Any one fails   →  blocked, logged, never fires          │
└─────────────────────────────────────────────────────────┘
```

| Dimension | What it controls | Who sets it | Where it's checked |
|-----------|-----------------|-------------|-------------------|
| **Scope** | Which tools the user can access | Admin (in UI or config) | Planner (invisible tools) + Gate (rejection) |
| **Intent** | How destructive those tools can be | User (per-session) or config (default) | Gate (impact ≤ intent) |
| **Clearance** | Which resources those tools can touch | External endpoint (delegated) | Gate (HTTP call before execute) |

---

## Scope

Scopes define **which tools** a user can use. Default deny — if a tool isn't listed, the user can't see it and the LLM can't plan with it.

### Model

```json
{
  "name": "operator",
  "description": "Normal work mode — all tools, destructive commands capped",
  "tools": ["*"],
  "cap": {"bash": 100, "git": 100}
}
```

**`tools`** — array of tool names, or `["*"]` for all. Everything not listed is denied.

**`cap`** — optional per-tool impact ceiling, expressed as a rank from the intent registry. `"bash": 100` means bash is allowed but capped at rank 100 (operate), so destructive bash commands (impact 200) are blocked even though bash itself is in scope.

### Built-in Scopes

Seeded on first run:

| Scope | Tools | Caps | Use case |
|-------|-------|------|----------|
| `observer` | sysinfo, file_read, file_list, web_search, web_fetch, process_list, net_info, env_list, disk_usage, memory_recall, memory_search, clipboard | — | Read-only access |
| `operator` | `*` (all) | bash:100, git:100 | Normal work, destructive commands capped |
| `full` | `*` (all) | — | Unrestricted |

### Three Layers of Defense

```
Layer 1: Planner only sees scoped tools
         → LLM can't plan with tools it doesn't know exist

Layer 2: Scheduler rejects unscoped tools
         → catches any planner bypass or hallucination

Layer 3: Gate caps impact per-tool
         → bash allowed but "rm -rf" blocked by cap
```

### Assignment

Users get scopes through:
1. **Direct assignment** — `user.scopes: ["operator"]`
2. **Group inheritance** — user is in group "engineering" which has scopes ["operator", "full"]

When a user has multiple scopes, they merge:
- **Tools**: union — tool is allowed if ANY scope allows it
- **Caps**: min — if two scopes cap the same tool, strictest wins

### API

```
GET    /api/v1/scopes           — list all scopes
POST   /api/v1/scopes           — create scope
PUT    /api/v1/scopes/{name}    — update scope
DELETE /api/v1/scopes/{name}    — delete scope
```

### Example

```json
{
  "name": "legal-assistant",
  "description": "File operations and research, no destructive commands",
  "tools": ["file_read", "file_write", "file_list", "web_search", "web_fetch",
            "archive", "memory_store", "memory_recall", "sysinfo"],
  "cap": {}
}
```

A user with this scope can read/write files and search the web, but cannot run bash commands, kill processes, or access git. Those tools are invisible to the planner.

---

## Intent

Intent controls **how hard** tools can hit. It's the user's per-session safety level.

### Levels

Intent names and ranks come from the intent registry. Three builtins ship by
default, and admins can insert custom intents at any rank between or above
them (see `docs/intents.md`).

| Rank | Builtin name | Impact allowed | Examples |
|------|--------------|----------------|----------|
| 0    | **observe**  | Read-only (impact 0) | sysinfo, file_read, web_search |
| 100  | **operate**  | + reversible side-effects (impact 0–100) | file_write, bash (non-destructive) |
| 200  | **override** | + destructive operations (impact 0–200) | bash (rm, kill), git push |

Custom intents slot in by rank. A `triage` intent at rank 50 would allow
anything with impact ≤ 50, blocking normal `operate` tools. The comparison
the gate performs is always `tool.impact ≤ intent.rank`, never a name match.

### How It's Set

- **Config default** — `agent.safety_level: 100` in kaiju.json (a rank, not an enum)
- **Per-user max** — user's `max_intent` rank caps what they can request
- **Per-session** — CLI: `/intent operate`, UI: dropdown in chat input
- **Per-request** — API: `{"intent": "override"}` in execute request. Any name
  registered in the intent registry is valid, including custom ones.

The effective intent is: `min(requested, user_max, config_default)`

### Gate Check

```
tool.Impact(params) ≤ min(intent, scope_cap)

bash({"command": "ls"})       → impact 0   ≤ 100 → allowed
bash({"command": "rm -rf /"}) → impact 200 > 100 → blocked
```

Impact is **dynamic per invocation** — the same tool returns different impact levels depending on its parameters. `bash("ls")` is impact 0, `bash("rm -rf /")` is impact 200.

---

## Clearance

Clearance delegates resource-level authorization to **external endpoints**. The gate calls a URL before executing a tool and respects the answer. If no endpoint is configured, the tool executes freely.

### Why External?

Kaiju can't know about every resource in every domain:
- A hospital's patient-ward access rules
- A drone's GPS-based zone boundaries
- A company's Active Directory group policies
- A filesystem's `.access` files

Instead of solving all of these, kaiju **delegates**. You point it at an endpoint that knows your domain, and it asks permission before every tool call.

### How It Works

```
1. Admin configures: bash → http://localhost:9090/authorize (timeout: 2s)

2. Agent fires: bash({"command": "rm -rf /data"})

3. Gate calls endpoint:
   POST http://localhost:9090/authorize
   Body: {"tool": "bash", "params": {"command": "rm -rf /data"}, "user": "alice"}

4. Endpoint returns: {"allow": false, "reason": "destructive command on production path"}

5. Gate blocks. Tool never fires. Reason logged in audit.
```

### Rules

| Condition | Result |
|-----------|--------|
| No endpoint configured for tool | **Allowed** (default open, opt-in security) |
| Endpoint returns `{"allow": true}` | Allowed |
| Endpoint returns `{"allow": false}` | **Blocked**, reason in audit log |
| Endpoint unreachable | **Blocked** (deny by default) |
| Endpoint times out | **Blocked** (deny by default) |
| Endpoint returns non-200 | **Blocked** |
| Endpoint returns invalid JSON | **Blocked** |

### Configuration

Configured per-tool in the UI or API:

```json
{
  "tool_name": "bash",
  "url": "http://localhost:9090/authorize",
  "timeout_ms": 2000,
  "headers": {"X-Service-Key": "secret"}
}
```

### API

```
GET    /api/v1/clearance           — list all endpoints
POST   /api/v1/clearance           — create/update endpoint
DELETE /api/v1/clearance/{tool}    — remove endpoint
```

### Endpoint Protocol

**Request** (POST, JSON):
```json
{
  "tool": "bash",
  "params": {"command": "rm -rf /data"},
  "user": "alice"
}
```

**Response** (200, JSON):
```json
{
  "allow": true
}
```
or
```json
{
  "allow": false,
  "reason": "destructive command on production path"
}
```

The endpoint receives the full tool params — it can inspect the command string, file path, URL, zone ID, or whatever is relevant to its domain. It returns a simple allow/deny. That's the entire contract.

### Examples

**Filesystem ACL** — a script that checks path permissions:
```json
{"tool_name": "file_write", "url": "http://localhost:9091/fs-check", "timeout_ms": 500}
```

**Drone zone control** — a local service with GPS + geofencing:
```json
{"tool_name": "navigate", "url": "http://drone-controller.local:8081/clearance", "timeout_ms": 500}
```

**Hospital RBAC** — an enterprise auth service:
```json
{"tool_name": "patient_lookup", "url": "https://rbac.hospital.internal/authorize", "timeout_ms": 3000}
```

**Active Directory** — a proxy that checks AD group membership:
```json
{"tool_name": "bash", "url": "https://ad-proxy.corp.com/kaiju-clearance", "timeout_ms": 2000, "headers": {"X-AD-Token": "${AD_TOKEN}"}}
```

### Performance

For latency-sensitive deployments (drones, real-time systems), run the clearance endpoint on **localhost**. HTTP over loopback is sub-millisecond. The timeout is configurable per-endpoint — set 500ms for local services, 3000ms for remote ones.

### Live Updates

Adding or removing endpoints via the API immediately updates the running gate. No restart needed.

---

## Full Gate Flow

When a tool fires, the gate checks in order:

```
Tool: bash({"command": "echo hello > output.txt"})
User: alice (scopes: ["operator"], intent: operate)

1. SCOPE CHECK
   Is "bash" in alice's resolved scope?
   → operator scope has tools: ["*"] → yes
   → Is there a cap? operator has bash:100

2. INTENT CHECK
   rank = registry.ResolveToolIntent("bash", bash, {"command": "echo hello > output.txt"}) → 100 (write pattern)
   ceiling = min(intent=100, scope_cap=100) → 100
   100 ≤ 100 → pass

3. CLEARANCE CHECK
   Is there an endpoint configured for "bash"?
   → yes: http://localhost:9090/authorize
   POST {"tool":"bash","params":{"command":"echo hello > output.txt"},"user":"alice"}
   → {"allow": true}
   → pass

4. EXECUTE
   bash runs "echo hello > output.txt"
   result logged in audit
```

If any step fails:
```
Step 1 fails → "gate: bash not in user scope"
Step 2 fails → "gate: bash blocked (impact=200 > ceiling=100)"
Step 3 fails → "clearance: bash — destructive command on production path"
```

The tool never fires. The error is returned to the aggregator which explains to the user why the action was blocked and what they'd need to do to proceed.

---

## Database Schema

```sql
-- Scopes
scopes (
  name        TEXT PRIMARY KEY,
  tools       TEXT NOT NULL DEFAULT '["*"]',  -- JSON array of tool names
  cap         TEXT NOT NULL DEFAULT '{}',     -- JSON object {tool: max_impact}
  description TEXT NOT NULL DEFAULT ''
)

-- Groups (assign scopes to multiple users)
groups (
  name        TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT '',
  profiles    TEXT NOT NULL DEFAULT '[]'      -- JSON array of scope names
)

-- Users
users (
  username    TEXT PRIMARY KEY,
  max_intent  INTEGER NOT NULL DEFAULT 100,   -- rank from the intent registry (0=observe, 100=operate, 200=override, or custom)
  scopes      TEXT NOT NULL DEFAULT '[]',     -- JSON array of scope names
  groups      TEXT NOT NULL DEFAULT '[]',     -- JSON array of group names
  ...
)

-- Clearance endpoints
clearance_endpoints (
  tool_name   TEXT PRIMARY KEY,
  url         TEXT NOT NULL,
  timeout_ms  INTEGER NOT NULL DEFAULT 2000,
  headers     TEXT NOT NULL DEFAULT '{}'       -- JSON object
)
```

---

## Summary

```
Authentication  →  who you are           →  JWT login
Scope           →  which tools           →  admin-defined, default deny
Intent          →  how destructive       →  user-set per session
Clearance       →  which resources       →  external endpoint, delegated

All checked at the gate. Tools don't know about any of this.
```
