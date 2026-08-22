// Package db provides a SQLite-backed storage layer for kaiju.
// Uses modernc.org/sqlite (pure Go, no CGO) for full cross-platform support.
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

/*
 * DB wraps a SQLite connection with kaiju-specific operations.
 * desc: Core database handle holding the connection and file path for all storage operations
 */
type DB struct {
	conn *sql.DB
	path string
}

/*
 * Open opens or creates a SQLite database at the given path.
 * desc: Initializes the SQLite connection with WAL mode, busy timeout, and foreign keys, then runs migrations
 * param: path - filesystem path where the SQLite database file will be created or opened
 * return: initialized DB handle and nil error, or nil and an error if directory creation, opening, or migration fails
 */
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("db: create dir: %w", err)
	}

	conn, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}

	// Connection pool settings for embedded use
	conn.SetMaxOpenConns(1) // SQLite is single-writer
	conn.SetMaxIdleConns(1)

	db := &DB{conn: conn, path: path}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("db: migrate: %w", err)
	}

	return db, nil
}

/*
 * Close closes the database connection.
 * desc: Shuts down the underlying sql.DB connection
 * return: error from the connection close, or nil on success
 */
func (d *DB) Close() error {
	return d.conn.Close()
}

/*
 * Conn returns the underlying sql.DB for advanced queries.
 * desc: Provides direct access to the raw sql.DB connection for operations not covered by helper methods
 * return: the underlying *sql.DB connection
 */
func (d *DB) Conn() *sql.DB {
	return d.conn
}

/*
 * migrate runs all schema migrations.
 * desc: Creates all required tables, indexes, and applies ALTER TABLE migrations idempotently
 * return: error if any migration statement fails
 */
func (d *DB) migrate() error {
	migrations := []string{
		// Users
		`CREATE TABLE IF NOT EXISTS users (
			username    TEXT PRIMARY KEY,
			pass_hash   TEXT NOT NULL,
			max_intent  INTEGER NOT NULL DEFAULT 1,
			scopes      TEXT NOT NULL DEFAULT '[]',
			disabled    INTEGER NOT NULL DEFAULT 0,
			created_at  INTEGER NOT NULL
		)`,

		// Scopes
		// No max_intent column. One was here, holding 2 for every scope on a scale
		// that runs 0, 100, 200 — a leftover from an older scale of 0, 1, 2 — and no
		// code ever read it. The ceiling a scope allows is intent_cap, added below.
		// A database made before this keeps the old column; nothing reads it and
		// SQLite makes dropping one awkward, so it is left alone.
		`CREATE TABLE IF NOT EXISTS scopes (
			name        TEXT PRIMARY KEY,
			description TEXT NOT NULL DEFAULT '',
			tools       TEXT NOT NULL DEFAULT '{}'
		)`,

		// Sessions (conversation history)
		`CREATE TABLE IF NOT EXISTS sessions (
			id          TEXT PRIMARY KEY,
			channel     TEXT NOT NULL,
			user_id     TEXT NOT NULL DEFAULT '',
			title       TEXT NOT NULL DEFAULT '',
			created_at  INTEGER NOT NULL,
			updated_at  INTEGER NOT NULL
		)`,

		// Messages (conversation messages)
		`CREATE TABLE IF NOT EXISTS messages (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			role        TEXT NOT NULL,
			content     TEXT NOT NULL,
			dag_trace   TEXT NOT NULL DEFAULT '',
			created_at  INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, created_at)`,

		// Investigations (DAG execution records)
		`CREATE TABLE IF NOT EXISTS investigations (
			id              TEXT PRIMARY KEY,
			node_id         TEXT NOT NULL DEFAULT '',
			trigger_type    TEXT NOT NULL DEFAULT '',
			started_at      INTEGER NOT NULL,
			completed_at    INTEGER NOT NULL DEFAULT 0,
			duration_ms     INTEGER NOT NULL DEFAULT 0,
			intent          TEXT NOT NULL DEFAULT '',
			dag_mode        TEXT NOT NULL DEFAULT '',
			nodes_count     INTEGER NOT NULL DEFAULT 0,
			llm_calls       INTEGER NOT NULL DEFAULT 0,
			reflection_count INTEGER NOT NULL DEFAULT 0,
			replan_count    INTEGER NOT NULL DEFAULT 0,
			outcome         TEXT NOT NULL DEFAULT '',
			status          TEXT NOT NULL DEFAULT ''
		)`,

		// Settings (key-value store for UI preferences, etc.)
		`CREATE TABLE IF NOT EXISTS settings (
			key         TEXT PRIMARY KEY,
			value       TEXT NOT NULL,
			updated_at  INTEGER NOT NULL
		)`,

		// Memories (long-term semantic + episodic, namespaced by user)
		`CREATE TABLE IF NOT EXISTS memories (
			id         TEXT PRIMARY KEY,
			namespace  TEXT NOT NULL,
			key        TEXT NOT NULL,
			content    TEXT NOT NULL,
			type       TEXT NOT NULL,
			tags       TEXT DEFAULT '[]',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_namespace ON memories(namespace)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_ns_type ON memories(namespace, type)`,

		// Clearance endpoints (external authorization services per tool)
		`CREATE TABLE IF NOT EXISTS clearance_endpoints (
			tool_name  TEXT PRIMARY KEY,
			url        TEXT NOT NULL,
			timeout_ms INTEGER NOT NULL DEFAULT 2000,
			headers    TEXT NOT NULL DEFAULT '{}'
		)`,

		// Scopes (default-deny tool permission sets)
		`CREATE TABLE IF NOT EXISTS scopes (
			name        TEXT PRIMARY KEY,
			description TEXT NOT NULL DEFAULT '',
			tools       TEXT NOT NULL DEFAULT '["*"]',
			cap         TEXT NOT NULL DEFAULT '{}'
		)`,
		// Groups (collections of scopes assigned to users)
		`CREATE TABLE IF NOT EXISTS groups (
			name        TEXT PRIMARY KEY,
			description TEXT NOT NULL DEFAULT '',
			profiles    TEXT NOT NULL DEFAULT '[]'
		)`,
		// Intents (configurable IGX levels, sparse rank ordering)
		`CREATE TABLE IF NOT EXISTS intents (
			name               TEXT PRIMARY KEY,
			rank               INTEGER NOT NULL,
			description        TEXT NOT NULL DEFAULT '',
			prompt_description TEXT NOT NULL DEFAULT '',
			is_builtin         INTEGER NOT NULL DEFAULT 0,
			is_default         INTEGER NOT NULL DEFAULT 0
		)`,
		// Tool intent overrides (tool name → intent name). Tools not in this
		// table fall back to their Go Impact() default.
		`CREATE TABLE IF NOT EXISTS tool_intents (
			tool_name   TEXT PRIMARY KEY,
			intent_name TEXT NOT NULL
		)`,
	}

	for _, m := range migrations {
		if _, err := d.conn.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, m)
		}
	}

	// Add profiles/groups columns to users (ignore error if already exists)
	alterMigrations := []string{
		`ALTER TABLE users ADD COLUMN profiles TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE users ADD COLUMN groups TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE messages ADD COLUMN dag_trace TEXT NOT NULL DEFAULT ''`,
		// Compaction used to delete what it summarised, so a conversation older
		// than the window was gone: not from the model's view, from the record.
		// It is now marked with the id of the summary that stands for it, so the
		// thread can still be read and searched, and a summary can say which
		// messages it replaced. Zero means the message is still live.
		`ALTER TABLE messages ADD COLUMN compacted_into INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE scopes ADD COLUMN cap TEXT NOT NULL DEFAULT '{}'`,
		// How far a run may go for someone holding this scope. Existing scopes
		// default to the top of the scale, so adding the column takes nothing away
		// from anybody who already has it — an administrator lowers it deliberately.
		`ALTER TABLE scopes ADD COLUMN intent_cap INTEGER NOT NULL DEFAULT 200`,
		`ALTER TABLE intents ADD COLUMN is_default INTEGER NOT NULL DEFAULT 0`,
		// runs.verdict became runs.outcome. A database created before the
		// rename has the old column and CREATE TABLE IF NOT EXISTS will not
		// touch it, so the insert would fail on a column that is not there.
		// Both statements are attempted and both are allowed to fail: on a new
		// database the rename finds nothing, on an old one the add finds the
		// column already present.
		`ALTER TABLE runs RENAME COLUMN verdict TO outcome`,
		`ALTER TABLE runs ADD COLUMN outcome TEXT NOT NULL DEFAULT ''`,
	}
	for _, m := range alterMigrations {
		d.conn.Exec(m) // ignore duplicate column errors
	}

	// Clean up legacy dag_trace messages stored as fake message rows
	d.conn.Exec(`DELETE FROM messages WHERE role = 'dag_trace'`)

	// Seed default profiles
	return d.SeedDefaultScopes()
}
