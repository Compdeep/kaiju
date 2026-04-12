# Memory System

Kaiju implements a multi-layered memory system following LangChain's memory framework. Memory is fully multi-tenant — every operation is scoped by user ID, with no cross-user data leakage.

## Architectural Boundary (Security)

**Memory lives at the chat boundary, never inside the execution layer.** This is a deliberate architectural rule, not a coincidence — it's an anti-prompt-injection security boundary.

The two and only legitimate access points are:

1. **Chat input** — `internal/api/api.go` `handleChat`. Loads conversation history into `Trigger.History`, prepends long-term semantic and episodic memories as a system message, and stores the user's incoming message.
2. **Chat output** — same handler, after the aggregator runs. Stores the assistant's verdict as the next message and may extract new facts.

The agent's **execution layer** (ContextGate, source implementations, graph nodes — executive, compute, reflector, debugger, observer, aggregator) **must never query or write memory directly**. ContextGate has no `memory` source by design, and graph node code must not import the `memory` package.

### Why this matters

The execution layer runs untrusted tool output through reasoning steps: bash command output, web fetches, compute/coder LLM responses, debugger plans. Any of those can contain adversarial content trying to manipulate the agent. If memory were reachable from execution-layer code, a malicious tool result could:

- **Exfiltrate** stored facts by causing them to be quoted in a subsequent LLM call that goes to a logging or network sink.
- **Rewrite** the user's memory by inducing the agent to call a hidden memory_store path with attacker-supplied content.

By keeping memory at the chat boundary, both reads and writes are attested by the authenticated user request itself. Untrusted tool content cannot reach memory because there is no code path from the execution layer to the memory package.

### The one exception: explicit memory tools

The LLM tools `memory_store`, `memory_recall`, and `memory_search` exist as deliberate, auditable actions the LLM can take, the same way it can call `bash` or `file_write`. They appear in the worklog as explicit tool calls. This is allowed because it requires the LLM to make an active decision rather than memory being injected automatically by code processing untrusted input.

### How to know if you're crossing the boundary

If you're adding code that reads or writes memory, ask: **"is this triggered by an authenticated user request, or is it triggered automatically by code that might be processing untrusted input?"** If the latter, do not add it. The right place is at the chat boundary in `api.go`, attested by the user's HTTP request itself.

## Memory Types

| Type | Scope | What it stores | Storage |
|------|-------|----------------|---------|
| **Short-term** | Per-session | Message history within a conversation | `sessions` + `messages` tables |
| **Long-term Semantic** | Per-user, cross-session | Facts: "user prefers Python", "prod DB is at db.internal:5432" | `memories` table, namespace `{user}/semantic` |
| **Long-term Episodic** | Per-user, cross-session | Experiences: "last deployment broke", "migration took 45 min" | `memories` table, namespace `{user}/episodic` |
| **Procedural** | Per-user | Self-modifying instructions (future) | `memories` table, namespace `{user}/procedural` |

## How It Works

### Short-term Memory (Conversation History)

Each conversation lives in a **session**. Every user message and agent response is stored in the `messages` table. Before each query, the last 50 messages are loaded and injected into every LLM call as conversation turns.

```
User sends message
  → Memory manager loads session history (last 50 messages)
  → Long-term memories loaded and injected as system context
  → History injected into ALL LLM calls:
      [system prompt, ...history, current query]
  → Agent responds
  → Both user message and response stored in session
  → If message count > 30, auto-compact triggers in background
```

The LLM sees the full conversation context — it knows what you said 5 messages ago, what tools were used, what decisions were made.

### Long-term Semantic Memory (Facts)

Facts that persist across all conversations. These are injected into the system prompt as a "Your Memory" section:

```
## Your Memory

### Known Facts
- **user-language**: Prefers Python over JavaScript
- **prod-database**: PostgreSQL at db.internal:5432
- **team-lead**: Alice manages the backend team

### Past Experiences
- Last Friday deployment caused a 2-hour outage, rollback was needed
- The CSV export script works better with pandas than raw file I/O
```

Facts can be stored three ways:
1. **Agent tool** — the agent has `memory_store` / `memory_recall` / `memory_search` tools and can decide to remember facts during conversation
2. **Explicit** — user says "remember that I prefer Python" or uses the UI
3. **API** — `POST /api/v1/memories`

### Long-term Episodic Memory (Experiences)

Same storage as semantic, but tagged as `episodic`. The difference:
- **Semantic** = what is true ("the server runs Ubuntu")
- **Episodic** = what happened ("when we ran the migration, it took 45 minutes and we had to rollback")

Episodic memories help the agent learn from past outcomes and avoid repeating mistakes.

## Compaction

When a session exceeds 30 messages, compaction summarizes old messages to keep the context window manageable:

1. Split messages: oldest N-10 to summarize, keep last 10 intact
2. LLM call summarizes the old messages into 2-3 paragraphs
3. Old messages deleted, summary inserted as a system message
4. Result: `[summary] + [last 10 messages]` — full context in fewer tokens

Compaction can be triggered:
- **Automatically** — after each response, if threshold exceeded (background goroutine)
- **Manually** — `/compact` in CLI, or compact button in UI, or `POST /api/v1/sessions/{id}/compact`

## Multi-tenant Isolation

Every memory operation is scoped by user ID extracted from JWT:

```
Namespace format: {user_id}/{type}

alice/semantic    — Alice's facts (only Alice can see)
alice/episodic    — Alice's experiences
bob/semantic      — Bob's facts (Alice cannot see these)
_global/semantic  — Shared facts (readable by all, writable by admins only)
```

**Enforcement at three levels:**

1. **API layer** — user ID extracted from JWT token, injected server-side into every query. The user cannot specify a different user ID.
2. **Memory manager** — constructed per-request with bound user ID: `memory.New(db, llm, userID)`. All operations automatically scoped.
3. **DB queries** — sessions filtered by `WHERE user_id = ?`, memories filtered by `WHERE namespace IN (...)` with only the user's namespaces.

Even if the LLM hallucinates and asks to "search all users' memories", the namespace filter is in compiled Go code — the model cannot override it.

## Commands

### CLI

| Command | Effect |
|---------|--------|
| `/new` | Start a fresh session |
| `/compact` | Summarize current conversation history |
| `/resume <id>` | Switch to a different session |
| `/threads` | List active sessions |
| `/remember <fact>` | Store a long-term semantic memory |
| `/forget <key>` | Delete a memory |

### API

```
# Sessions
POST   /api/v1/sessions              — create new session
GET    /api/v1/sessions              — list sessions (user-scoped)
DELETE /api/v1/sessions/{id}         — delete session + messages
GET    /api/v1/sessions/{id}/messages — get conversation history
POST   /api/v1/sessions/{id}/compact  — force compaction

# Memories
POST   /api/v1/memories              — store a memory
GET    /api/v1/memories?q=&type=     — search memories
DELETE /api/v1/memories/{id}         — forget a memory

# Execution (with memory)
POST   /api/v1/execute
{
  "query": "what did we discuss yesterday?",
  "session_id": "abc-123"
}
```

When `session_id` is included in the execute request, the memory system automatically:
1. Loads conversation history from that session
2. Loads long-term memories for the user
3. Injects both into the agent's LLM calls
4. Stores the user message and agent response
5. Triggers auto-compaction if needed

## UI

### Session Sidebar

The chat page has a left sidebar showing all sessions. Click to switch, delete button to remove. "New Chat" button creates a fresh session.

### Compact Button

In the chat input bar, a compress icon triggers manual compaction of the current session.

### Memories Tab

In the admin modal, a "memories" tab shows all stored long-term memories with:
- Search by keyword
- Filter by type (semantic/episodic)
- Store new memories
- Delete individual memories

## Architecture

```
┌──────────────────────────────────────────────────┐
│                  Memory Manager                   │
│           internal/memory/manager.go              │
│                                                   │
│  Per-request, user-scoped. Bridges DB + LLM.     │
└──────┬─────────────────┬─────────────────┬───────┘
       │                 │                 │
  ┌────▼────┐    ┌───────▼──────┐   ┌──────▼──────┐
  │ Short-  │    │  Long-term   │   │  Compactor  │
  │ term    │    │  (memories   │   │  (LLM call  │
  │ (msgs)  │    │   table)     │   │  to summary)│
  └────┬────┘    └───────┬──────┘   └──────┬──────┘
       │                 │                 │
       └────────┬────────┘                 │
                │                          │
         ┌──────▼──────┐                   │
         │   SQLite    │◄──────────────────┘
         │  kaiju.db   │
         └─────────────┘
```

**Injection into agent pipeline:**

```
History loaded from DB
  → Injected as conversation turns: [system, ...history, query]
  → Planner sees full context
  → ReAct fallback sees full context
  → Aggregator sees full context
  → Direct LLM fallback sees full context
```

All four LLM call sites in the agent use `BuildMessagesWithHistory()` which prepends history between the system prompt and the current query.

## Database Schema

```sql
-- Sessions (conversation containers)
sessions (
  id          TEXT PRIMARY KEY,
  channel     TEXT NOT NULL,
  user_id     TEXT NOT NULL,
  title       TEXT DEFAULT '',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
)

-- Messages (conversation turns)
messages (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id  TEXT REFERENCES sessions(id) ON DELETE CASCADE,
  role        TEXT NOT NULL,      -- "user", "assistant", "system"
  content     TEXT NOT NULL,
  created_at  INTEGER NOT NULL
)

-- Memories (long-term, namespaced)
memories (
  id          TEXT PRIMARY KEY,
  namespace   TEXT NOT NULL,      -- "{user_id}/semantic", "_global/semantic"
  key         TEXT NOT NULL,
  content     TEXT NOT NULL,
  type        TEXT NOT NULL,      -- "semantic", "episodic", "procedural"
  tags        TEXT DEFAULT '[]',  -- JSON array
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
)
```

## Configuration

Compaction settings (currently hardcoded, configurable in future):

| Setting | Default | Description |
|---------|---------|-------------|
| Compact threshold | 30 messages | Auto-compact triggers above this count |
| Keep recent | 10 messages | Messages preserved after compaction |
| Max history | 50 messages | Maximum messages loaded per query |
| LLM temperature for compaction | 0.3 | Low creativity for factual summaries |
