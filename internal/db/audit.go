package db

import (
	"time"
)

/*
 * AuditEntry represents an IBE gate decision log entry.
 * desc: Records the outcome of an Intent-Based Enforcement gate check including tool, impact level, and result
 */
type AuditEntry struct {
	ID        int64  `json:"id"`
	Timestamp int64  `json:"timestamp"`
	Username  string `json:"username"`
	Tool      string `json:"tool"`
	Params    string `json:"params"`
	Intent    int    `json:"intent"`
	Impact    int    `json:"impact"`
	Result    string `json:"result"`
	Error     string `json:"error"`
	AlertID   string `json:"alert_id"`
}

/*
 * InsertAudit logs a gate decision.
 * desc: Inserts an audit log row, auto-setting timestamp to now if zero
 * param: e - the AuditEntry to record (Timestamp defaults to current time if 0)
 * return: error on insertion failure, nil on success
 */
func (d *DB) InsertAudit(e AuditEntry) error {
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().Unix()
	}
	_, err := d.conn.Exec(
		`INSERT INTO audit_log (timestamp, username, skill, params, intent, impact, result, error, alert_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Timestamp, e.Username, e.Tool, e.Params, e.Intent, e.Impact, e.Result, e.Error, e.AlertID,
	)
	return err
}

/*
 * QueryAudit returns recent audit entries, newest first.
 * desc: Queries audit log rows ordered by timestamp descending, defaulting to 100 if limit is non-positive
 * param: limit - maximum number of entries to return (defaults to 100 if <= 0)
 * return: slice of AuditEntry and nil error, or nil and an error on query failure
 */
func (d *DB) QueryAudit(limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.conn.Query(
		`SELECT id, timestamp, username, skill, params, intent, impact, result, error, alert_id
		 FROM audit_log ORDER BY timestamp DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Username, &e.Tool, &e.Params,
			&e.Intent, &e.Impact, &e.Result, &e.Error, &e.AlertID); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

/*
 * QueryAuditByUser returns audit entries for a specific user.
 * desc: Queries audit log rows filtered by username, ordered by timestamp descending
 * param: username - the user whose audit entries to retrieve
 * param: limit - maximum number of entries to return (defaults to 100 if <= 0)
 * return: slice of AuditEntry and nil error, or nil and an error on query failure
 */
func (d *DB) QueryAuditByUser(username string, limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.conn.Query(
		`SELECT id, timestamp, username, skill, params, intent, impact, result, error, alert_id
		 FROM audit_log WHERE username = ? ORDER BY timestamp DESC LIMIT ?`, username, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Username, &e.Tool, &e.Params,
			&e.Intent, &e.Impact, &e.Result, &e.Error, &e.AlertID); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

/*
 * InsertInvestigation records a completed DAG investigation.
 * desc: Upserts an investigation row capturing DAG execution metrics and outcome
 * param: id - unique investigation identifier
 * param: nodeID - the originating node ID
 * param: triggerType - what triggered the investigation (e.g. user request, alert)
 * param: startedAt - unix timestamp when the investigation began
 * param: completedAt - unix timestamp when the investigation finished
 * param: durationMs - total duration in milliseconds
 * param: intent - the intent classification string
 * param: dagMode - the DAG execution mode used
 * param: nodesCount - number of nodes executed in the DAG
 * param: llmCalls - total LLM API calls made
 * param: reflectionCount - number of reflection cycles performed
 * param: replanCount - number of replan cycles performed
 * param: verdict - final verdict or conclusion
 * param: status - completion status (e.g. "completed", "failed")
 * return: error on insertion failure, nil on success
 */
func (d *DB) InsertInvestigation(id, nodeID, triggerType string, startedAt, completedAt, durationMs int64,
	intent, dagMode string, nodesCount, llmCalls, reflectionCount, replanCount int, verdict, status string) error {
	_, err := d.conn.Exec(
		`INSERT OR REPLACE INTO investigations
		 (id, node_id, trigger_type, started_at, completed_at, duration_ms, intent, dag_mode,
		  nodes_count, llm_calls, reflection_count, replan_count, verdict, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, nodeID, triggerType, startedAt, completedAt, durationMs, intent, dagMode,
		nodesCount, llmCalls, reflectionCount, replanCount, verdict, status,
	)
	return err
}

// Settings key-value store

/*
 * SetSetting stores a key-value pair.
 * desc: Upserts a row in the settings table with the current timestamp
 * param: key - the setting key
 * param: value - the setting value
 * return: error on upsert failure, nil on success
 */
func (d *DB) SetSetting(key, value string) error {
	_, err := d.conn.Exec(
		`INSERT OR REPLACE INTO settings (key, value, updated_at) VALUES (?, ?, ?)`,
		key, value, time.Now().Unix(),
	)
	return err
}

/*
 * GetSetting retrieves a value by key.
 * desc: Looks up a single setting value from the settings table
 * param: key - the setting key to look up
 * return: the setting value and nil error, or empty string and an error if not found
 */
func (d *DB) GetSetting(key string) (string, error) {
	var value string
	err := d.conn.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	return value, err
}
