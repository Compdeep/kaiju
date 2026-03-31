# Kaiju API Reference

Kaiju exposes a REST API and an SSE event stream. The API powers the web UI and is available to external consumers — custom frontends, automation scripts, external agents, or domain-specific control systems.

Base URL: `http://localhost:8080` (default, configurable via `kaiju.json`)

## Authentication

Most endpoints require a JWT Bearer token. Obtain one via `/api/v1/auth/login`.

```
Authorization: Bearer <token>
```

Tokens expire after 24 hours. Public endpoints (health, login, config, SSE) don't require auth.

---

## Execution

### POST `/api/v1/execute`

Execute a query through the DAG agent engine.

**Request:**
```json
{
  "query": "find all open ports on 10.0.0.0/24",
  "session_id": "sess-abc123",
  "intent": "operate",
  "mode": "reflect"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `query` | string | yes | The user's request |
| `session_id` | string | no | Conversation session for memory context |
| `intent` | string | no | `observe`, `operate`, or `override`. Default: `auto` |
| `mode` | string | no | DAG mode: `reflect`, `nReflect`, or `orchestrator` |

**Response:**
```json
{
  "verdict": "Found 3 open ports on 10.0.0.5: 22 (SSH), 80 (HTTP), 443 (HTTPS)...",
  "gaps": ["nmap not installed"],
  "dag_id": "dag-1234",
  "duration_ms": 4200
}
```

| Field | Type | Description |
|-------|------|-------------|
| `verdict` | string | Final synthesized response |
| `gaps` | string[] | Capability gaps the planner identified |
| `dag_id` | string | DAG execution ID |
| `duration_ms` | int | Total execution time |
| `error` | string | Error message if execution failed |

### POST `/api/v1/interject`

Send a message into a running DAG execution (human-in-the-loop).

**Request:**
```json
{"message": "focus on port 443, skip the others"}
```

**Response:**
```json
{"sent": true}
```

### GET `/api/v1/status`

Agent status and configuration summary.

**Response:**
```json
{
  "status": "ready",
  "dag_mode": "reflect",
  "safety_level": 1,
  "tool_count": 15
}
```

### GET `/api/v1/tools`

List all registered tools and skills.

**Response:**
```json
[
  {
    "name": "bash",
    "description": "Execute a shell command...",
    "impact": 1,
    "isBuiltin": true,
    "enabled": true,
    "source": "builtin"
  },
  {
    "name": "kaiju_coder",
    "description": "Coding workflows...",
    "impact": 0,
    "isBuiltin": false,
    "enabled": true,
    "source": "skillmd:/home/sites/kaiju/skills/bundled/kaiju_coder/SKILL.md"
  }
]
```

---

## Sessions

### POST `/api/v1/sessions`

Create a new conversation session.

**Request:**
```json
{"channel": "web"}
```

**Response:**
```json
{"id": "sess-abc123"}
```

### GET `/api/v1/sessions`

List sessions (most recent 50).

**Response:**
```json
[
  {"id": "sess-abc123", "user_id": "admin", "source": "web", "title": "Port scan analysis", "created_at": "2026-03-27T10:00:00Z"}
]
```

### GET `/api/v1/sessions/{id}/messages`

Get conversation history for a session (up to 500 messages).

**Response:**
```json
[
  {"id": "msg-1", "session_id": "sess-abc123", "role": "user", "content": "scan the network"},
  {"id": "msg-2", "session_id": "sess-abc123", "role": "assistant", "content": "Found 3 hosts...", "dag_trace": "[...]"}
]
```

### POST `/api/v1/sessions/{id}/compact`

Compact session history — summarizes old messages to reduce context size.

**Response:**
```json
{"summary": "Previous conversation covered network scanning of 10.0.0.0/24..."}
```

### POST `/api/v1/sessions/{id}/trace`

Save a DAG trace for a session (used by the frontend to persist trace visualization).

**Request:**
```json
{"nodes": [{"id": "n1", "skill": "bash", "state": "resolved", ...}]}
```

### DELETE `/api/v1/sessions/{id}`

Delete a session and its messages.

---

## Memory

### POST `/api/v1/memories`

Store a long-term memory (semantic fact or episodic experience).

**Request:**
```json
{
  "key": "network-topology",
  "content": "Production network uses 10.0.0.0/24 with gateway at .1",
  "type": "semantic",
  "tags": ["network", "production"]
}
```

### GET `/api/v1/memories?q=network&type=semantic`

Search memories by query string, optionally filtered by type.

### DELETE `/api/v1/memories/{id}`

Delete a memory by ID.

---

## Users, Scopes, Groups

### Users

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/users` | List all users |
| POST | `/api/v1/users` | Create user: `{username, password, max_intent, profiles}` |
| PUT | `/api/v1/users/{username}` | Update user: `{max_intent?, profiles?, groups?, disabled?}` |
| DELETE | `/api/v1/users/{username}` | Delete user |

### Scopes

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/scopes` | List all scopes |
| POST | `/api/v1/scopes` | Create scope: `{name, description}` |
| PUT | `/api/v1/scopes/{name}` | Update scope |
| DELETE | `/api/v1/scopes/{name}` | Delete scope |

### Groups

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/groups` | List all groups |
| POST | `/api/v1/groups` | Create group: `{name, description}` |
| PUT | `/api/v1/groups/{name}` | Update group |
| DELETE | `/api/v1/groups/{name}` | Delete group |

### Clearance Endpoints

External authorization delegation — tools can require clearance from an external endpoint before execution.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/clearance` | List clearance endpoints |
| POST | `/api/v1/clearance` | Upsert: `{tool_name, url, timeout_ms?, headers?}` |
| DELETE | `/api/v1/clearance/{tool}` | Delete clearance endpoint |

---

## Configuration

### GET `/api/v1/config`

Get current configuration (sensitive values masked).

### PATCH `/api/v1/config`

Update LLM, executor, or agent configuration at runtime.

**Request:**
```json
{
  "llm": {"provider": "anthropic", "model": "claude-sonnet-4-20250514"},
  "agent": {"dag_mode": "nReflect", "safety_level": 2}
}
```

### GET `/api/v1/models`

List supported LLM models across all providers.

---

## SSE Event Stream

### GET `/events`

Server-Sent Events stream for real-time DAG execution updates. No authentication required.

```
GET /events
Accept: text/event-stream
```

Events are JSON objects on `data:` lines:

```
data: {"type":"start","alert":"dag-1234","nodes":[...]}

data: {"type":"node","id":"n1","node":{"id":"n1","state":"running","skill":"bash",...}}

data: {"type":"node","id":"n1","node":{"id":"n1","state":"resolved","result":"...","actions":[...]}}

data: {"type":"verdict","text":"Based on"}

data: {"type":"done","nodes":[...]}
```

### Event Types

| Type | When | Key Fields |
|------|------|------------|
| `start` | Investigation begins | `alert` (DAG ID), `nodes` (initial snapshot) |
| `node` | Node state changes (pending → running → resolved/failed) | `id`, `node` (full NodeInfo with `actions`) |
| `add` | New node added to DAG (replan, observer injection) | `id`, `node` |
| `verdict` | Streaming aggregator output token | `text` (chunk to append) |
| `done` | Investigation complete | `nodes` (final snapshot) |

### NodeInfo Schema

```json
{
  "id": "n3",
  "type": "skill",
  "state": "resolved",
  "tag": "scan target host",
  "skill": "bash",
  "deps": ["n1", "n2"],
  "spawn": "",
  "ms": 1542,
  "err": null,
  "err_type": null,
  "result_size": 384,
  "result": "PORT   STATE SERVICE\n22/tcp open  ssh\n80/tcp open  http...",
  "summary": "3 open ports found",
  "params": "{\"command\":\"nmap -sT 10.0.0.5\"}",
  "impact": 1,
  "source": "builtin",
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

### Node Actions

Actions are side-effects attached to node results. See [actions.md](actions.md) for the full reference.

Frontends route actions by `type`:

| Action Type | Behavior |
|-------------|----------|
| `panel_show` | Open content in the composable panel |
| (future) `notify` | Show a notification |
| (future) `navigate` | Switch view or focus |
| (future) `trigger` | Invoke another tool |

Actions are extensible — domain-specific deployments define their own types and route them in their frontend or control system.

---

## Health

### GET `/health`

```json
{"status": "ok"}
```

---

## Error Format

All error responses follow:

```json
{"error": "descriptive error message"}
```

HTTP status codes: `400` (bad request), `401` (unauthorized), `404` (not found), `500` (internal error).
