---
name: session_logs
description: "Search and analyze past conversation sessions. Use when the user asks about previous conversations, wants to find something discussed earlier, or needs to review session history."
---

## When to Use

Use when the user asks to:
- Find something from a previous conversation
- List recent sessions
- Search across session history
- Review what was discussed in a specific session
- Analyze patterns across conversations

## Planning Guidance

### List recent sessions

1. `bash` — `kaiju` uses SQLite, query via the API:
   ```
   curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/sessions | head -50
   ```

Or if running in the same context, the session list is available through the sessions store.

### Search session messages

Sessions and messages are stored in the SQLite database at `~/.kaiju/kaiju.db`:

1. `bash` — `sqlite3 ~/.kaiju/kaiju.db "SELECT session_id, role, substr(content, 1, 200) FROM messages WHERE content LIKE '%search term%' ORDER BY created_at DESC LIMIT 20"`

### Get full conversation from a session

1. `bash` — `sqlite3 ~/.kaiju/kaiju.db "SELECT role, content FROM messages WHERE session_id='<id>' ORDER BY created_at"`

### Analyze session patterns

Plan parallel queries:

1. `bash` — `sqlite3 ~/.kaiju/kaiju.db "SELECT COUNT(*) as total, date(created_at) as day FROM sessions GROUP BY day ORDER BY day DESC LIMIT 14"` (sessions per day)
2. `bash` — `sqlite3 ~/.kaiju/kaiju.db "SELECT COUNT(*) FROM messages WHERE role='user'"` (total user messages, parallel)

### Find sessions by topic

1. `bash` — search messages content:
   ```
   sqlite3 ~/.kaiju/kaiju.db "SELECT DISTINCT s.id, s.title, s.created_at FROM sessions s JOIN messages m ON s.id = m.session_id WHERE m.content LIKE '%topic%' ORDER BY s.created_at DESC LIMIT 10"
   ```

### What NOT to do

- Don't read the SQLite file with `file_read` — use `sqlite3` CLI or the REST API
- Don't scan all messages sequentially — use SQL WHERE clauses to filter
- Don't modify the database — session_logs is read-only
