package db

import (
	"time"
)

/*
 * Session represents a conversation.
 * desc: Tracks a chat session with its channel, owning user, title, and timestamps
 */
type Session struct {
	ID        string `json:"id"`
	Channel   string `json:"channel"`
	UserID    string `json:"user_id"`
	Title     string `json:"title"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

/*
 * Message represents a single message in a conversation.
 * desc: Stores one chat message with its role, content, optional DAG trace, and timestamp
 */
type Message struct {
	ID        int64  `json:"id"`
	SessionID string `json:"session_id"`
	Role      string `json:"role"` // "user", "assistant", "system"
	Content   string `json:"content"`
	DAGTrace  string `json:"dag_trace,omitempty"` // JSON DAG trace, set on assistant messages
	CreatedAt int64  `json:"created_at"`
}

/*
 * CreateSession creates a new conversation session.
 * desc: Inserts a session row with the current time as both created_at and updated_at
 * param: id - unique session identifier
 * param: channel - the channel or interface this session belongs to
 * param: userID - the owning user's identifier
 * param: title - display title for the session
 * return: error on insertion failure, nil on success
 */
func (d *DB) CreateSession(id, channel, userID, title string) error {
	now := time.Now().Unix()
	_, err := d.conn.Exec(
		`INSERT INTO sessions (id, channel, user_id, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, channel, userID, title, now, now,
	)
	return err
}

/*
 * GetSession returns a session by ID.
 * desc: Looks up a single session row by primary key
 * param: id - the session ID to look up
 * return: pointer to the Session and nil error, or nil and an error if not found
 */
func (d *DB) GetSession(id string) (*Session, error) {
	row := d.conn.QueryRow(
		`SELECT id, channel, user_id, title, created_at, updated_at FROM sessions WHERE id = ?`, id,
	)
	var s Session
	if err := row.Scan(&s.ID, &s.Channel, &s.UserID, &s.Title, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

/*
 * ListSessions returns recent sessions, newest first.
 * desc: Queries sessions ordered by updated_at descending, defaulting to 50 if limit is non-positive
 * param: limit - maximum number of sessions to return (defaults to 50 if <= 0)
 * return: slice of Sessions and nil error, or nil and an error on query failure
 */
func (d *DB) ListSessions(limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.conn.Query(
		`SELECT id, channel, user_id, title, created_at, updated_at FROM sessions ORDER BY updated_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.Channel, &s.UserID, &s.Title, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

/*
 * DeleteSession removes a session and all its messages.
 * desc: Deletes the session row; cascading foreign key removes associated messages
 * param: id - the session ID to delete
 * return: error on query failure, nil on success
 */
func (d *DB) DeleteSession(id string) error {
	_, err := d.conn.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

/*
 * AddMessage adds a message to a session and updates the session timestamp.
 * desc: Inserts a message row and bumps the session's updated_at to the current time
 * param: sessionID - the session to add the message to
 * param: role - message role ("user", "assistant", or "system")
 * param: content - the message text content
 * return: error on insertion or update failure, nil on success
 */
func (d *DB) AddMessage(sessionID, role, content string) error {
	now := time.Now().Unix()
	_, err := d.conn.Exec(
		`INSERT INTO messages (session_id, role, content, created_at) VALUES (?, ?, ?, ?)`,
		sessionID, role, content, now,
	)
	if err != nil {
		return err
	}
	_, err = d.conn.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, now, sessionID)
	return err
}

/*
 * GetMessages returns all messages for a session in chronological order.
 * desc: Queries messages for a session ordered by created_at, defaulting to 1000 if limit is non-positive
 * param: sessionID - the session whose messages to retrieve
 * param: limit - maximum number of messages to return (defaults to 1000 if <= 0)
 * return: slice of Messages and nil error, or nil and an error on query failure
 */
func (d *DB) GetMessages(sessionID string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := d.conn.Query(
		`SELECT id, session_id, role, content, dag_trace, created_at FROM messages WHERE session_id = ? ORDER BY created_at LIMIT ?`,
		sessionID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.DAGTrace, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// ─── User-scoped methods (multi-tenant) ─────────────────────────────────────

/*
 * ListSessionsForUser returns sessions owned by a specific user.
 * desc: Queries sessions filtered by user_id, ordered by updated_at descending, defaulting to 50 if limit is non-positive
 * param: userID - the user whose sessions to retrieve
 * param: limit - maximum number of sessions to return (defaults to 50 if <= 0)
 * return: slice of Sessions and nil error, or nil and an error on query failure
 */
func (d *DB) ListSessionsForUser(userID string, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.conn.Query(
		`SELECT id, channel, user_id, title, created_at, updated_at FROM sessions WHERE user_id = ? ORDER BY updated_at DESC LIMIT ?`,
		userID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.Channel, &s.UserID, &s.Title, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

/*
 * GetSessionForUser returns a session only if owned by the given user.
 * desc: Looks up a session by ID with an additional user_id filter for multi-tenant access control
 * param: id - the session ID to look up
 * param: userID - the user who must own the session
 * return: pointer to the Session and nil error, or nil and an error if not found or not owned by userID
 */
func (d *DB) GetSessionForUser(id, userID string) (*Session, error) {
	row := d.conn.QueryRow(
		`SELECT id, channel, user_id, title, created_at, updated_at FROM sessions WHERE id = ? AND user_id = ?`,
		id, userID,
	)
	var s Session
	if err := row.Scan(&s.ID, &s.Channel, &s.UserID, &s.Title, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

/*
 * MessageCount returns the number of messages in a session.
 * desc: Counts all message rows belonging to the given session
 * param: sessionID - the session to count messages for
 * return: message count and nil error, or zero and an error on failure
 */
func (d *DB) MessageCount(sessionID string) (int, error) {
	var count int
	err := d.conn.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, sessionID).Scan(&count)
	return count, err
}

/*
 * UpdateSessionTitle sets the session title.
 * desc: Updates the title column for the given session ID
 * param: id - the session ID to update
 * param: title - the new title string
 * return: error on query failure, nil on success
 */
func (d *DB) UpdateSessionTitle(id, title string) error {
	_, err := d.conn.Exec(`UPDATE sessions SET title = ? WHERE id = ?`, title, id)
	return err
}

/*
 * DeleteOldestMessages removes all but the keepNewest messages from a session.
 * desc: Deletes messages not in the most recent keepNewest set, used for context window compaction
 * param: sessionID - the session to trim
 * param: keepNewest - number of most recent messages to retain
 * return: error on query failure, nil on success
 */
func (d *DB) DeleteOldestMessages(sessionID string, keepNewest int) error {
	_, err := d.conn.Exec(
		`DELETE FROM messages WHERE session_id = ? AND id NOT IN (
			SELECT id FROM messages WHERE session_id = ? ORDER BY created_at DESC LIMIT ?
		)`, sessionID, sessionID, keepNewest,
	)
	return err
}

/*
 * SetDAGTrace saves a DAG trace on the most recent assistant message in a session.
 * desc: Updates the dag_trace column on the latest assistant-role message for the given session
 * param: sessionID - the session containing the target message
 * param: trace - JSON string of the DAG execution trace
 * return: error on query failure, nil on success
 */
func (d *DB) SetDAGTrace(sessionID, trace string) error {
	_, err := d.conn.Exec(
		`UPDATE messages SET dag_trace = ? WHERE id = (
			SELECT id FROM messages WHERE session_id = ? AND role = 'assistant' ORDER BY created_at DESC LIMIT 1
		)`, trace, sessionID,
	)
	return err
}

/*
 * PrependMessage inserts a message with a specific timestamp (for compaction summaries).
 * desc: Inserts a message row with a caller-provided created_at timestamp instead of using the current time
 * param: sessionID - the session to add the message to
 * param: role - message role ("user", "assistant", or "system")
 * param: content - the message text content
 * param: createdAt - unix timestamp to set as the message creation time
 * return: error on insertion failure, nil on success
 */
func (d *DB) PrependMessage(sessionID, role, content string, createdAt int64) error {
	_, err := d.conn.Exec(
		`INSERT INTO messages (session_id, role, content, created_at) VALUES (?, ?, ?, ?)`,
		sessionID, role, content, createdAt,
	)
	return err
}
