---
name: session_logs
description: "Search and analyse past conversation sessions. Use when the user asks about previous conversations, wants to find something discussed earlier, or needs to review session history."
---

## When to Use

Use when the user asks to:
- Find something from an earlier conversation
- Recall what was said or decided before
- Search across session history
- Review what was discussed in a particular session
- Look at patterns across conversations

## Planning Guidance

The `message_search` tool reads stored conversation messages. It reaches messages older than the ones in front of you, including any a summary has replaced, and it is the right step for almost every question of this kind. It takes:

- `query` — the words to look for. Every word must appear in a message for it to match, and matching is by stem, so "process" finds "processes".
- `scope` — `session` searches this conversation only, `all` searches every conversation. It defaults to `session`, so a question about a *previous* conversation needs `all` set explicitly.
- `limit` — up to 50, best match first. Defaults to 10.

### Find something from an earlier conversation

1. `message_search` — `{"query": "<the distinctive words>", "scope": "all", "limit": 20}`

One step. Answer from what it returns.

### Recall something from this conversation

1. `message_search` — `{"query": "<the distinctive words>"}`

The default scope is this session, so nothing more is needed.

### Search on more than one phrasing

The same subject is often written two ways, and every word in a query must match. Where a single query might miss, plan a search for each phrasing. They pass nothing to each other, so they run at the same time.

1. `message_search` — `{"query": "deploy rollback", "scope": "all"}`
2. `message_search` — `{"query": "revert release", "scope": "all"}`

### Counting and aggregating across sessions

`message_search` returns matching messages, not totals. A question that asks how many sessions there were, or how activity changed over time, needs the database. It is a SQLite file named `kaiju.db` in the data directory, which is set by configuration and is not the same path on every install. Find it before querying it:

1. `bash` — locate the file, tag `find_db`, for example `ls -la "$KAIJU_DATA_DIR/kaiju.db" 2>/dev/null || find "$HOME" -maxdepth 4 -name kaiju.db 2>/dev/null | head -1`
2. `bash` — run the query against the path `find_db` returned, for example `sqlite3 <path> "SELECT date(created_at) AS day, COUNT(*) FROM sessions GROUP BY day ORDER BY day DESC LIMIT 14"`

Never write a `~/.kaiju/kaiju.db` path into a step without finding it first. That is the default location, not the only one.

## RULES

- Reach for `message_search` before the database. It needs no path, no credentials and no SQL, and it is what this system provides for the purpose.
- Set `scope: "all"` when the question is about an earlier conversation. Leaving it at the default searches only the current one and returns nothing, which reads as "it was never discussed".
- Never read `kaiju.db` with `file_read`. It is a binary SQLite file; use the `sqlite3` command.
- Never write to the database. Reading session history is observation, and a write here damages the record of every conversation.
- Do not invent an API endpoint, port or token to reach sessions over HTTP. Use the tool.
