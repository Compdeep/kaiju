# Kaiju API Reference

Kaiju exposes a REST API and an SSE event stream. The API powers the web UI and is available to external consumers — custom frontends, automation scripts, external agents, or domain-specific control systems (makeen is one such host).

## Base URL & ports

`kaiju serve` runs a single HTTP listener — the **gateway** — on `channels.web.port` (default **`8080`**). That one server fronts everything: the web UI, the `/events` SSE stream, and every REST endpoint in this document. Against a default install the base URL is therefore:

```
http://localhost:8080
```

The config also carries a dedicated API block, `api` (`api.enabled`, default **`false`**; `api.port`, default **`8081`**). It is **off by default** — there is no separate API listener in the default serve path, and the REST surface is reached through the gateway on `8080`. So `8080` is the gateway/web port, *not* a standalone API port, and `8081` is the reserved (disabled) API port.

## Authentication

Most endpoints require a JWT Bearer token. Obtain one via `POST /api/v1/auth/login`.

```
Authorization: Bearer <token>
```

Tokens expire after 24 hours. Two endpoints also accept the token as a `?token=` query parameter, because a browser can't set an `Authorization` header on an `EventSource` or an `<img>`/`<iframe>` src: the SSE stream (`/events`) and the workspace file server (`/api/v1/workspace/serve`).

Endpoints that need **no** auth: `GET /health`, `POST /api/v1/auth/login`, the config endpoints (`GET`/`PATCH /api/v1/config`, `GET /api/v1/models` — open so the UI can complete first-run setup), the live-preview server (`GET /api/v1/workspace/live/…`, whose sub-resources can't carry a token), and the web UI itself (`/`). Everything else under `/api/v1/` requires a Bearer token.

---

## Execution

### POST `/api/v1/execute`

Run a query. This is the single entry point for **all** answer lanes — the executive/DAG agent, plain chat, and vision — selected by the fields below. There is **no** separate `/chat` endpoint: conversational turns ride this route with `chat_mode: true`.

**Request:**
```json
{
  "query": "find all open ports on 10.0.0.0/24",
  "session_id": "sess-abc123",
  "intent": "operate",
  "mode": "reflect",
  "agg_mode": -1
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `query` | string | yes* | The user's request. *Optional when `regenerate` is true (the query is taken from history). |
| `session_id` | string | no | Conversation session for memory/history and per-session uploads. |
| `intent` | string | no | Any intent name registered in the intent registry (see `GET /api/v1/intents`). When omitted, the planner infers the level from tool impacts; the result is still capped by the caller's token/scope ceiling. |
| `mode` | string | no | Execution mode: `reflect`, `nReflect`, or `orchestrator`. |
| `agg_mode` | int | no | Aggregator mode: `0` = skip, `1` = executor model, `2` = reasoning model. Omit (or send `-1`) for auto — the reflector decides. |
| `execution_mode` | string | no | Per-request override: `interactive` or `autonomous`. |
| `provider` | string | no | Heavy-lane (answer/reasoning) provider: `openai`\|`anthropic`\|`openrouter`\|`selfhosted`. Empty ⇒ configured default. |
| `model` | string | no | Heavy-lane model id. |
| `executor_provider` | string | no | Light-lane (classify/route/reflect) provider. |
| `executor_model` | string | no | Light-lane model id. |
| `vision_provider` | string | no | Vision-lane provider (answers image questions directly). |
| `vision_model` | string | no | Vision-lane model id. Empty ⇒ configured default vision model. |
| `chat_mode` | bool | no | When true, the turn is answered by a direct completion — no planner/DAG/tools — for plain conversation and non-tool (roleplay) models. |
| `chat_provider` | string | no | Chat-lane provider override. |
| `chat_model` | string | no | Chat-lane model override. Empty ⇒ configured chat default, else the reasoning model. |
| `chat_tools` | string[] | no | Tool palette available **if** a chat turn escalates to the agent. It does *not* give the chat model a tool loop; the signed token grant is the authority on what an escalated run may reach. |
| `agent` | bool\|null | no | Chat→agent escalation permission, consulted only when `chat_mode` is true. `null`/omitted ⇒ **default: may escalate**; `true` ⇒ explicit allow; `false` ⇒ pure chat, never escalates. To run the agent directly, use execute mode (`chat_mode:false`), not this flag. |
| `regenerate` | bool | no | Re-run the last turn: the previous assistant reply is dropped and the last user message is answered again. Session-scoped and ownership-checked; `query` is ignored. |

The keys for each provider live in kaiju's config, never in the request — the host only sends the *selection*.

**Vision routing.** If the session has image uploads and a vision model is configured/selected, the image question is answered directly by that model (no planner/tools) so a tool-less vision model still works. `chat_mode` normally takes precedence, but if the chat model can't see images the turn is re-routed to the vision lane so the image isn't dropped.

**Response:**
```json
{
  "verdict": "Found 3 open ports on 10.0.0.5: 22 (SSH), 80 (HTTP), 443 (HTTPS)...",
  "actions": [{"tool": "nmap", "params": {"target": "10.0.0.6"}}],
  "gaps": ["nmap not installed"],
  "dag_id": "api-1730000000000000000",
  "nodes": 5,
  "llm_calls": 3,
  "tokens": 8421,
  "tokens_in": 6120,
  "tokens_out": 2301,
  "duration_ms": 4200
}
```

| Field | Type | Description |
|-------|------|-------------|
| `verdict` | string | Final synthesized response |
| `actions` | object[] | Recommended follow-up actions from the aggregator (caller decides). Each is `{tool, params}`. |
| `gaps` | string[] | Capability gaps the planner identified (missing tools) |
| `dag_id` | string | DAG execution ID (`api-<unixnano>`) |
| `nodes` | int | Total DAG nodes executed |
| `llm_calls` | int | Total LLM round-trips |
| `tokens` | int64 | Total LLM tokens for this request |
| `tokens_in` | int64 | Prompt tokens (for host-side cost split) |
| `tokens_out` | int64 | Completion tokens |
| `duration_ms` | int64 | Total execution time |
| `error` | string | Error message if execution failed |

### POST `/api/v1/oneshot`

A single provider-routed LLM completion that bypasses the agent entirely — no preflight, planner, DAG, tools, reflection, or aggregator. For hosts that need a raw completion routed through kaiju's provider keys (e.g. makeen's compliance LLM-detection stage). Token usage is still attributed to the caller.

**Request:**
```json
{
  "messages": [{"role": "user", "content": "classify this text"}],
  "provider": "openai",
  "model": "gpt-4.1-mini",
  "temperature": 0.2,
  "max_tokens": 512,
  "images": ["data:image/png;base64,..."]
}
```

`messages` is required. `temperature` is clamped to `0–2`; `max_tokens` is clamped to `1–8192` (0 or out-of-range ⇒ 1024). `images` (https URLs or `data:` URIs) attach to the latest user message for a vision model; total payload is bounded (~18 MB).

**Response:**
```json
{"content": "…", "tokens": 128}
```

### POST `/api/v1/interject`

Send a message into a running DAG execution (human-in-the-loop).

**Request:**
```json
{"session_id": "sess-abc123", "message": "focus on port 443, skip the others"}
```

**Response:** `{"sent": true}` — or `{"sent": false, "reason": "no active investigation"}` if nothing is running.

### GET `/api/v1/status`

Agent status and configuration summary.

**Response:**
```json
{"status": "idle", "dag_mode": "orchestrator", "safety_level": 100, "tool_count": 15}
```

### GET `/api/v1/tools`

List all registered tools and skills.

**Response:**
```json
[
  {
    "name": "bash",
    "description": "Execute a shell command...",
    "default_impact": 1,
    "enabled": true,
    "source": "builtin"
  },
  {
    "name": "kaiju_coder",
    "description": "Coding workflows...",
    "default_impact": 0,
    "enabled": true,
    "source": "skillmd:/home/sites/kaiju/skills/bundled/kaiju_coder/SKILL.md"
  }
]
```

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Tool name |
| `description` | string | Tool description |
| `default_impact` | int | The tool's default impact/intent rank |
| `enabled` | bool | Whether the tool is enabled |
| `source` | string | `builtin`, `custom`, or `skillmd:<path>` |

### GET `/api/v1/usage`

In-memory LLM token-usage tallies since process start, broken down per `(principal, category)`. In-memory only — resets on restart.

**Response:**
```json
{"usage": {"admin": {"reasoning": 12000, "executor": 3400}}, "total": 15400}
```

---

## Sessions & messages

### POST `/api/v1/sessions`

Create a new conversation session for the authenticated user. Returns `201`.

**Response:** `{"id": "sess-abc123"}`

### GET `/api/v1/sessions`

List the caller's sessions (most recent 50, newest first).

**Response:**
```json
[
  {"id": "sess-abc123", "user_id": "admin", "source": "web", "title": "Port scan analysis", "created_at": "2026-03-27T10:00:00Z"}
]
```

### DELETE `/api/v1/sessions/{id}`

Delete a session (ownership-checked) and its messages.

### GET `/api/v1/sessions/{id}/messages`

Conversation history for a session the caller owns (up to 500 messages, including `dag_trace` entries).

**Response:**
```json
[
  {"id": 1, "session_id": "sess-abc123", "role": "user", "content": "scan the network"},
  {"id": 2, "session_id": "sess-abc123", "role": "assistant", "content": "Found 3 hosts...", "dag_trace": "[...]"}
]
```

### PATCH `/api/v1/sessions/{id}/messages/{msgId}`

Overwrite one message's content (no LLM — a direct edit to stored history, for either the user's or the assistant's turn). Ownership-checked and scoped to `(session, message)`.

**Request:** `{"content": "corrected text"}`

### DELETE `/api/v1/sessions/{id}/messages/{msgId}`

Delete a message **and everything after it** — used to remove the last turn (including unsticking a turn whose reply never came back) or to truncate a chat at a point. Ownership-checked.

### POST `/api/v1/sessions/{id}/compact`

Compact session history — summarizes old messages to reduce context size.

**Response:** `{"summary": "Previous conversation covered network scanning of 10.0.0.0/24..."}`

### POST `/api/v1/sessions/{id}/trace`

Save a DAG trace onto the session's most recent assistant message (the frontend uses this to persist trace visualization).

**Request:** `{"nodes": [{"id": "n1", "tool": "bash", "state": "resolved"}]}`

---

## Uploads (per session)

Chat attachments run through the synchronous uploads pipeline (validate → write → extract metadata → optional summary → memory entry). Images from a session feed the vision lane on `/execute`.

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/sessions/{id}/uploads` | Multipart upload (form field `file`). Returns the upload Result (size, lines, inline content for tiny files). Bounded at the server max file size. |
| GET | `/api/v1/sessions/{id}/uploads` | List a session's uploads (restores the chip strip on reload). |
| DELETE | `/api/v1/sessions/{id}/uploads/{name}` | Remove one upload (file + sidecars) and its memory entry. |

---

## Workspace

File browser / server for the agent's workspace directory. All paths are validated to stay inside the workspace.

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/api/v1/workspace/files` | Bearer | Directory listing. Optional `?path=` for sub-directory navigation. |
| GET | `/api/v1/workspace/serve` | Bearer or `?token=` | Stream a single file (`?path=` required) with its MIME type, for viewing/downloading. |
| GET | `/api/v1/workspace/live/{filepath...}` | none | Serve files from `workspace/project/` with path-based URLs, so a multi-file webapp's relative imports (`./style.css`, `./app.js`) resolve. Sub-resources can't carry a token, so this is unauthenticated. |
| POST | `/api/v1/workspace/write` | — | Write content to a workspace file (`{path, content}`). **Note:** this handler is registered on the API but is *not* mounted on the gateway in the current `kaiju serve` path, so it is not reachable as-is. |

---

## Memory

### POST `/api/v1/memories`

Store a long-term memory (semantic fact or episodic experience), scoped to the caller. Returns `201`.

**Request:**
```json
{
  "key": "network-topology",
  "content": "Production network uses 10.0.0.0/24 with gateway at .1",
  "type": "semantic",
  "tags": ["network", "production"]
}
```

`key` and `content` are required; `type` defaults to `semantic`.

### GET `/api/v1/memories?q=network&type=semantic`

Search the caller's memories by query string, optionally filtered by type (up to 50 results).

### DELETE `/api/v1/memories/{id}`

Delete a memory by ID (ownership-checked).

---

## Authentication endpoints

### POST `/api/v1/auth/login`

Exchange credentials for a signed JWT. **Not** JWT-protected.

**Request:** `{"username": "admin", "password": "…"}`

**Response:**
```json
{"token": "<jwt>", "expires_at": "2026-07-25T10:00:00Z", "username": "admin", "max_intent": 200}
```

`401` on invalid credentials.

### GET `/api/v1/auth/me`

Current authenticated user's profile.

**Response:** `{"username": "admin", "max_intent": 200, "scopes": ["ops"]}`

---

## Users, Scopes, Groups, Clearance

JWT-protected management endpoints.

### Users

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/users` | List all users |
| POST | `/api/v1/users` | Create user: `{username, password, max_intent, scopes}` |
| PUT | `/api/v1/users/{username}` | Update user: `{max_intent?, scopes?, groups?, disabled?}` |
| DELETE | `/api/v1/users/{username}` | Delete user |

### Scopes

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/scopes` | List all scopes |
| POST | `/api/v1/scopes` | Create scope |
| PUT | `/api/v1/scopes/{name}` | Replace scope by name |
| DELETE | `/api/v1/scopes/{name}` | Delete scope |

### Groups

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/groups` | List all groups |
| POST | `/api/v1/groups` | Create group |
| PUT | `/api/v1/groups/{name}` | Replace group by name |
| DELETE | `/api/v1/groups/{name}` | Delete group |

### Clearance endpoints

External authorization delegation — a tool can require clearance from an external endpoint before it executes.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/clearance` | List clearance endpoints |
| POST | `/api/v1/clearance` | Upsert: `{tool_name, url, timeout_ms?, headers?}` (live-applied to the checker) |
| DELETE | `/api/v1/clearance/{tool}` | Delete a clearance endpoint |

---

## Intents & tool intents

Intents are configurable levels with sparse integer ranks and per-intent prompt descriptions. Builtins can't be deleted and their rank is fixed. Changes require a restart to take effect.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/intents` | List all intents |
| POST | `/api/v1/intents` | Create an intent (never builtin) |
| PUT | `/api/v1/intents/{name}` | Update an intent |
| DELETE | `/api/v1/intents/{name}` | Delete an intent (`403` if builtin) |
| GET | `/api/v1/tool-intents` | Per-tool assignment table: `[{tool_name, intent_name, default_intent, has_override}]` |
| PUT | `/api/v1/tool-intents/{tool}` | Assign an intent to a tool: `{intent_name}` (overrides the tool's Go `Impact()` default) |
| DELETE | `/api/v1/tool-intents/{tool}` | Remove a tool's intent override |

---

## Configuration

Available **without** JWT (needed for initial setup via the UI).

### GET `/api/v1/config`

Current configuration with secrets masked (API key partially redacted, JWT secret and auth token blanked).

### PATCH `/api/v1/config`

Apply a partial config patch — only provided fields change. LLM/executor/vision/chat/route changes are hot-applied to the running agent, then the whole config is saved to disk.

**Request:**
```json
{
  "llm": {"provider": "anthropic", "model": "claude-sonnet-4-20250514"},
  "executor": {"model": "openai/gpt-4.1-mini"},
  "vision": {"provider": "openrouter", "model": "qwen/qwen-2.5-vl-72b-instruct"},
  "chat": {"provider": "openrouter", "model": "sao10k/l3.3-euryale-70b", "tools": []},
  "agent": {"dag_enabled": true, "dag_mode": "nReflect", "safety_level": 200, "route_provider": "openrouter", "route_model": "openai/gpt-4.1-mini"}
}
```

**Response:** `{"status": "updated"}`

### GET `/api/v1/models`

The supported model catalog across all providers. Each entry: `{id, name, provider, context?, available, vision?, chat?}`. `available` is true iff that provider is configured with a key in kaiju's config (hosts filter to available ∩ org-enabled). `vision` marks image-capable models; `chat` marks models suited to the tool-less chat lane.

---

## SSE Event Stream

### GET `/events`

Server-Sent Events stream for real-time DAG execution updates.

```
GET /events?token=<jwt>
Accept: text/event-stream
```

**Authenticated and per-principal filtered.** The DAG event bus is process-global, so isolation is enforced at the source: a caller only receives events for sessions it owns (events with no session, or another principal's session, are dropped). Pass the token via `?token=` since an `EventSource` can't set an `Authorization` header. The stream opens with a `: connected` keepalive comment.

Events are JSON objects on `data:` lines:

```
data: {"type":"start","alert":"api-1730...","nodes":[...]}

data: {"type":"node","id":"n1","node":{"id":"n1","state":"running","tool":"bash",...}}

data: {"type":"node","id":"n1","node":{"id":"n1","state":"resolved","result":"...","actions":[...]}}

data: {"type":"verdict","text":"Based on"}

data: {"type":"done","nodes":[...]}
```

### Event types

| Type | When | Key fields |
|------|------|------------|
| `start` | Investigation begins | `alert` (DAG ID), `nodes` (initial snapshot) |
| `node` | Node state changes (pending → running → resolved/failed) | `id`, `node` (full NodeInfo with `actions`) |
| `add` | New node added to the DAG (replan, observer injection) | `id`, `node` |
| `verdict` | Streaming aggregator output token | `text` (chunk to append) |
| `done` | Investigation complete | `nodes` (final snapshot) |

Every event also carries `session_id` (used for the ownership filter above).

### NodeInfo schema

```json
{
  "id": "n3",
  "type": "skill",
  "state": "resolved",
  "tag": "scan target host",
  "tool": "bash",
  "deps": ["n1", "n2"],
  "spawn": "",
  "ms": 1542,
  "err": "",
  "err_type": "",
  "result_size": 384,
  "result": "PORT   STATE SERVICE\n22/tcp open  ssh\n80/tcp open  http...",
  "summary": "3 open ports found",
  "params": "{\"command\":\"nmap -sT 10.0.0.5\"}",
  "impact": 1,
  "source": "builtin",
  "skills": ["kaiju_coder"],
  "tokens_in": 210,
  "tokens_out": 96,
  "started_at": "Apr 13 11:30:05",
  "actions": [
    {
      "type": "panel_show",
      "plugin": "code",
      "title": "scan-results.txt",
      "path": "/tmp/scan-results.txt"
    }
  ]
}
```

The node's tool is the `tool` field. `err`/`err_type` are omitted when empty.

### Node actions

Actions are side-effects attached to node results. See [actions.md](actions.md) for the full reference.

Frontends route actions by `type`:

| Action Type | Behavior |
|-------------|----------|
| `panel_show` | Open content in the composable panel |
| `notify` | Show a notification (uses the `message` field) |
| (future) `navigate` | Switch view or focus |
| (future) `trigger` | Invoke another tool |

A `NodeAction` carries `type`, and optionally `plugin`, `title`, `path`, `content`, `mime`, `line`, and `message`. Actions are extensible — domain-specific deployments define their own types and route them in their frontend or control system.

---

## Health

### GET `/health`

```json
{"status": "ok"}
```

---

## Error format

All error responses follow:

```json
{"error": "descriptive error message"}
```

HTTP status codes: `400` (bad request), `401` (unauthorized), `403` (forbidden), `404` (not found), `409` (conflict), `413` (payload too large), `500` (internal error), `503` (service unavailable).
