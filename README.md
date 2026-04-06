# Kaiju

A general-purpose AI assistant and agent framework with a web UI, file/media browser, DAG-based parallel execution, intent-gated safety, and a modular skill system inspired by OpenClaw. Kaiju separates reasoning from execution, enabling parallel tool dispatch, structural safety enforcement, and adaptive replanning — while remaining a practical, everyday assistant.

MIT License

## What it does

Kaiju is two things: a **conversational AI assistant** with a modern web interface, and an **execution kernel** that manages how the AI uses tools safely and efficiently.

As an assistant, it provides a chat UI with session history, a composable side panel (file browser, media viewer, code preview, canvas), configurable execution modes, and support for custom skills that extend its capabilities.

As an execution kernel, it separates planning from execution. The LLM produces a dependency graph of tool calls upfront, then the execution layer schedules, gates, and adapts the graph independently. Tools fire in parallel where dependencies allow. Reflection checkpoints evaluate intermediate results and replan when needed. An intent-based execution gate (IBE) enforces tool authorization at runtime without LLM involvement.

## Quick start

```bash
# Build (requires Go 1.25+, Node.js)
make build

# Configure
cp kaiju.json.example kaiju.json
# Edit kaiju.json — set your LLM API key

# Run
./kaiju serve              # Start daemon (web UI + API on :8080)
./kaiju chat               # Interactive CLI
./kaiju run "your query"   # One-shot query
```

## Web UI

The web interface includes:
- **Chat** with session history, streaming responses, and live DAG trace visualization
- **File browser** for the workspace directory
- **Media viewer** with floating player (keyboard: Space=play/pause, F=fullscreen, Esc=close)
- **Code preview** with syntax-aware display
- **Composable panel** with tabbed plugins (files, media, canvas, code, preview)
- **Configurable controls** — execution mode, IBE intent level, and aggregator mode selectable from the input bar

## Configuration

Kaiju reads configuration from (in priority order):
1. `--config <path>` flag
2. `KAIJU_CONFIG` environment variable
3. `./kaiju.json`
4. `~/.kaiju/config.json`

Minimal configuration:

```json
{
  "llm": {
    "provider": "openai",
    "api_key": "sk-...",
    "model": "gpt-4.1"
  },
  "agent": {
    "dag_enabled": true,
    "dag_mode": "reflect",
    "safety_level": 1,
    "data_dir": "~/.kaiju",
    "workspace": "~/.kaiju/workspace"
  },
  "channels": {
    "web": { "enabled": true, "port": 8080 }
  }
}
```

### LLM providers

- **OpenAI** — `gpt-4.1`, `gpt-4o`, `gpt-4.1-mini`
- **Anthropic** — `claude-sonnet-4`, `claude-haiku-4-5`
- **OpenRouter** — any model via `openrouter` provider

An optional executor model (cheaper, used for reflection/aggregation) can be configured separately:

```json
{
  "executor": {
    "model": "gpt-4.1-mini"
  }
}
```

## Execution modes

| Mode | How it works | Best for |
|------|-------------|----------|
| **Reflect** | Reflection checkpoints between dependency waves | Predictable, lowest LLM calls |
| **nReflect** | Reflection every N node completions | Balanced throughput and oversight |
| **Orchestrator** | Per-node observer evaluates each result | Highest quality, most thorough |
| **React** | Sequential reason-act-observe loop (for benchmarking) | Baseline comparison |

## Intent-Based Execution (IBE)

Every tool call passes through a four-variable gate before execution. The gate formula `impact ≤ min(intent, clearance, scope_cap)` is enforced at tool execution time in compiled code:

| Variable | Question | Set by |
|----------|----------|--------|
| **Scope** | Which tools? | Policy (admin-defined allowlists with per-tool impact caps) |
| **Intent** | What level? | Caller — a rank from the intent registry (default: 0/100/200, extensible via config/UI) |
| **Impact** | How dangerous? | Tool author (declared at compile time, parameter-aware) |
| **Clearance** | Approved? | External authority (HTTP endpoint) |

The gate runs in compiled code. The LLM does not observe gate decisions — blocked tools appear as generic failures, preventing adversarial probing of the safety policy.

### Delegated clearance via API

The clearance variable delegates authorization to an external HTTP endpoint. The gate sends `{tool, params, user}` and respects the boolean response. This enables domain-specific authorization logic (hospital ward access, drone geofences, enterprise AD policies) without any domain logic in the agent. Endpoints are configurable per-tool:

```json
{
  "clearance_endpoints": [
    { "tool": "bash", "url": "http://localhost:9090/approve", "timeout_ms": 500 }
  ]
}
```

## Multi-tenant user support

Kaiju supports multiple users with independent scopes and intent ceilings:

- Users authenticate via JWT (login endpoint or API token)
- Each user has a max intent level and one or more scopes controlling which tools they can access
- Scopes are composable — multiple scopes merge by union (tools) and minimum (caps)
- The first user created is automatically assigned admin scope
- Users are managed via the web UI (Users & Scopes tab) or CLI (`kaiju user add`)
- Currently backed by a local SQLite database; future versions will support syncing with OS-level user directories (Linux PAM, Windows AD)

## Built-in tools

| Tool | Impact | Description |
|------|--------|-------------|
| `bash` | 0-2 | Shell commands (impact varies by command content) |
| `web_search` | 0 | Web search (Startpage + DuckDuckGo fallback) |
| `web_fetch` | 0 | Fetch URLs with content extraction (markdown, text, summary) |
| `file_read` | 0 | Read files |
| `file_write` | 1 | Write/create files |
| `file_list` | 0 | List directory contents |
| `git` | 0-1 | Git operations |
| `sysinfo` | 0 | System information |
| `process_list` | 0 | Running processes |
| `process_kill` | 2 | Terminate processes |
| `net_info` | 0 | Network interfaces |
| `env_list` | 0 | Environment variables (secrets masked) |
| `disk_usage` | 0 | Disk space |
| `clipboard` | 0-1 | System clipboard |
| `archive` | 1 | Create/extract archives |
| `memory_store` | 1 | Persistent memory |
| `memory_recall` | 0 | Query memories |
| `memory_search` | 0 | Search memories |
| `panel_push` | 0 | Push content to UI panel |

## Skills

Skills are SKILL.md files that teach the agent domain-specific strategies, similar to OpenClaw's skill system. They hot-reload from configured directories and can include planning guidance, approach selection matrices, and tool usage recipes.

### Bundled skills

`kaiju_coder` `playwright` `kaiju_display` `kaiju_canvas` `github` `gh_issues` `download` `healthcheck` `precise_research` `summarize` `session_logs` `video_frames` `weather` `web_research_guide` `general_assistant` `kaiju_clawhub` `skill_creator` `tmux`

### Custom skills

Create a `SKILL.md` file in any configured skills directory:

```markdown
---
name: my_skill
description: "What this skill does"
---

## When to Use
Use when the user asks to...

## Planning Guidance
1. First, use `web_search` to find...
2. Then, use `bash` to process...
```

Skills are guidance — they teach the planner how to use existing tools for specific tasks. Skills with `CommandDispatch` can also wrap tools with custom execution logic.

## API

### Execute a query

```bash
curl -X POST http://localhost:8080/api/v1/execute \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"query": "your question", "intent": "operate", "mode": "reflect", "agg_mode": -1}'
```

### Key endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/execute` | Run query through DAG engine |
| `POST` | `/api/v1/interject` | Send message to running investigation |
| `GET` | `/api/v1/tools` | List available tools |
| `GET` | `/api/v1/status` | Agent status |
| `POST` | `/api/v1/sessions` | Create conversation session |
| `GET` | `/api/v1/sessions` | List sessions |
| `GET` | `/api/v1/sessions/{id}/messages` | Conversation history |
| `POST` | `/api/v1/auth/login` | Get JWT token |
| `GET` | `/api/v1/workspace/files` | Browse workspace files |
| `GET` | `/api/v1/workspace/serve` | Serve workspace file content |
| `GET` | `/events` | SSE stream for live DAG events |
| `PATCH` | `/api/v1/config` | Update config at runtime |

Full API documentation: [docs/api.md](docs/api.md)

## Architecture

```
Reasoning Layer             Execution Layer (Executive Kernel)
─────────────────          ──────────────────────────────────
 User ←→ Chat UI            Planner (reasoning model)
         ↕                      ↓
     REST API  ──────→  DAG Scheduler
         ↕                  ↓     ↓
     SSE Stream         Tools    Reflection (executor model)
                         ↓         ↓
                     IBE Gate   Micro-Planner
                         ↓         ↓
                     Execute    Replan/Skip
                         ↓
                     Aggregator (optional)
                         ↓
                     Verdict ──→ User
```

The LLM is invoked at discrete points (plan, reflect, aggregate) with no visibility into scheduling, gating, or dispatch mechanics. Each LLM call operates on bounded context — reflections see current-wave evidence, not full conversation history. Data flows from the reasoning layer to the execution layer at call time and back only at return time, creating a closed execution loop.

## Build

```bash
make build              # Full build (frontend + backend)
make build-linux        # Cross-compile for Linux
make build-darwin       # Cross-compile for macOS
make build-windows      # Cross-compile for Windows
```

Requires:
- Go 1.25+
- Node.js (for web UI build)

## Project structure

```
cmd/kaiju/          Entry point (chat, serve, run, skill, user commands)
internal/
  agent/            DAG engine (planner, scheduler, reflection, aggregator)
  api/              REST API handlers
  tools/            Built-in tools (bash, file, web, git, etc.)
  gateway/          HTTP server, WebSocket, SSE, JWT auth
  db/               SQLite (users, sessions, memories, audit)
  config/           Configuration loading
  channels/         Channel plugins (CLI, web, Telegram, Discord)
  memory/           Session history + semantic memory
  auth/             JWT service
  workspace/        Workspace bootstrapping
skills/bundled/     19 bundled skills
web/                Vue 3 frontend (Vite + Pinia)
docs/               Architecture, API, config, authorization docs
benchmarks/         GAIA, solar triage, and DAG vs ReAct benchmarks
```

## Documentation

- [Architecture](docs/architecture.md)
- [API Reference](docs/api.md)
- [Configuration](docs/config.md)
- [Authorization & IBE](docs/authorization.md)
- [IBE Examples](docs/examples-igx.md)
- [Memory System](docs/memory.md)
- [Workspace](docs/workspace.md)
- [Academic Paper](/paper.html)

## License

MIT
