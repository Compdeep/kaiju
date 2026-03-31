# Kaiju

A general-purpose AI agent framework with DAG-based parallel execution, intent-based safety gates, and modular tool/skill architecture.

MIT License

## What it does

Kaiju separates planning from execution. The LLM produces a dependency graph of tool calls upfront, then the execution layer schedules, gates, and adapts the graph independently. Tools fire in parallel where dependencies allow. Reflection checkpoints evaluate intermediate results and replan when needed. An intent-based execution gate enforces tool authorization at runtime without LLM involvement.

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
    "data_dir": "~/.kaiju"
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

## Intent-Based Execution (IBE)

Every tool call passes through a four-variable gate before execution:

| Variable | Question | Set by |
|----------|----------|--------|
| **Scope** | Which tools? | Policy (admin-defined allowlists) |
| **Intent** | What level? | Caller (0=observe, 1=operate, 2=override) |
| **Impact** | How dangerous? | Tool author (declared at compile time) |
| **Clearance** | Approved? | External authority (HTTP endpoint) |

The gate runs in compiled code. The LLM does not observe gate decisions — blocked tools appear as generic failures, preventing adversarial probing of the safety policy.

## Built-in tools

| Tool | Impact | Description |
|------|--------|-------------|
| `bash` | 0-2 | Shell commands (impact varies by command content) |
| `web_search` | 0 | Web search (Startpage + DuckDuckGo) |
| `web_fetch` | 0 | Fetch URLs with content extraction |
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

Skills are SKILL.md files that teach the agent domain-specific strategies. They hot-reload from configured directories.

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

Skills are guidance — they teach the planner how to use existing tools for specific tasks.

## API

### Execute a query

```bash
curl -X POST http://localhost:8080/api/v1/execute \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"query": "your question", "intent": "operate"}'
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
| `GET` | `/events` | SSE stream for live DAG events |
| `PATCH` | `/api/v1/config` | Update config at runtime |

Full API documentation: [docs/api.md](docs/api.md)

## Architecture

```
Presentation Layer          Execution Layer
─────────────────          ─────────────────
 User ←→ Chat UI            Planner (LLM)
         ↕                      ↓
     REST API  ──────→  DAG Scheduler
         ↕                  ↓     ↓
     SSE Stream         Tools    Reflection
                         ↓         ↓
                     IBE Gate   Micro-Planner
                         ↓         ↓
                     Execute    Replan/Skip
                         ↓
                     Aggregator (LLM)
                         ↓
                     Verdict ──→ User
```

The LLM is invoked at discrete points (plan, reflect, aggregate) with no visibility into scheduling, gating, or dispatch mechanics. Each LLM call operates on bounded context — reflections see current-wave evidence, not full conversation history.

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
skills/bundled/     19 bundled skills
web/                Vue 3 frontend (Vite + Pinia)
docs/               Architecture, API, config, authorization docs
```

## Documentation

- [Architecture](docs/architecture.md)
- [API Reference](docs/api.md)
- [Configuration](docs/config.md)
- [Authorization & IBE](docs/authorization.md)
- [Memory System](docs/memory.md)

## License

MIT
