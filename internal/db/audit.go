package db

import (
	"time"
)

/*
 * InsertInvestigation records a completed DAG investigation.
 * desc: Upserts an investigation row capturing DAG execution metrics and outcome
 * param: id - unique investigation identifier
 * param: nodeID - the originating node ID
 * param: triggerType - what started the run (e.g. a user request, an event)
 * param: startedAt - unix timestamp when the investigation began
 * param: completedAt - unix timestamp when the investigation finished
 * param: durationMs - total duration in milliseconds
 * param: intent - the intent classification string
 * param: dagMode - the DAG execution mode used
 * param: nodesCount - number of nodes executed in the DAG
 * param: llmCalls - total LLM API calls made
 * param: reflectionCount - number of reflection cycles performed
 * param: replanCount - number of replan cycles performed
 * param: outcome - final outcome or conclusion
 * param: status - completion status (e.g. "completed", "failed")
 * return: error on insertion failure, nil on success
 */
func (d *DB) InsertInvestigation(id, nodeID, triggerType string, startedAt, completedAt, durationMs int64,
	intent, dagMode string, nodesCount, llmCalls, reflectionCount, replanCount int, outcome, status string) error {
	_, err := d.conn.Exec(
		`INSERT OR REPLACE INTO investigations
		 (id, node_id, trigger_type, started_at, completed_at, duration_ms, intent, dag_mode,
		  nodes_count, llm_calls, reflection_count, replan_count, outcome, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, nodeID, triggerType, startedAt, completedAt, durationMs, intent, dagMode,
		nodesCount, llmCalls, reflectionCount, replanCount, outcome, status,
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
