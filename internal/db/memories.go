package db

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

/*
 * Memory represents a long-term memory entry (semantic fact or episodic experience).
 * desc: Namespaced key-value memory with type classification and tags, used for semantic, episodic, and procedural recall
 */
type Memory struct {
	ID        string   `json:"id"`
	Namespace string   `json:"namespace"` // "{user_id}/semantic", "{user_id}/episodic", "_global/semantic"
	Key       string   `json:"key"`
	Content   string   `json:"content"`
	Type      string   `json:"type"` // "semantic", "episodic", "procedural"
	Tags      []string `json:"tags"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}

/*
 * StoreMemory upserts a memory entry.
 * desc: Inserts or replaces a memory row, auto-generating an ID if empty and setting timestamps
 * param: m - the Memory to store (ID is auto-generated if blank, UpdatedAt is always set to now)
 * return: error if the upsert fails, nil on success
 */
func (d *DB) StoreMemory(m Memory) error {
	if m.ID == "" {
		m.ID = uuid.New().String()
	}
	now := time.Now().Unix()
	if m.CreatedAt == 0 {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	tagsJSON, _ := json.Marshal(m.Tags)

	_, err := d.conn.Exec(
		`INSERT OR REPLACE INTO memories (id, namespace, key, content, type, tags, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Namespace, m.Key, m.Content, m.Type, string(tagsJSON), m.CreatedAt, m.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("db: store memory: %w", err)
	}
	return nil
}

/*
 * GetMemory returns a memory by ID.
 * desc: Looks up a single memory row by primary key
 * param: id - the memory ID to look up
 * return: pointer to the Memory and nil error, or nil and an error if not found
 */
func (d *DB) GetMemory(id string) (*Memory, error) {
	row := d.conn.QueryRow(
		`SELECT id, namespace, key, content, type, tags, created_at, updated_at FROM memories WHERE id = ?`, id,
	)
	return scanMemory(row)
}

/*
 * SearchMemories finds memories matching a query across the given namespaces and types.
 * desc: Filters by namespace, type, and substring match against key/content/tags, ordered by updated_at descending
 * param: namespaces - list of namespaces to search within (all if empty)
 * param: query - substring to match against key, content, and tags (no filter if empty)
 * param: types - list of memory types to filter by (all if nil)
 * param: limit - maximum number of results to return (defaults to 50 if <= 0)
 * return: slice of matching Memories and nil error, or nil and an error on query failure
 */
func (d *DB) SearchMemories(namespaces []string, query string, types []string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 50
	}

	// Build WHERE clause
	var conditions []string
	var args []any

	// Namespace filter
	if len(namespaces) > 0 {
		placeholders := make([]string, len(namespaces))
		for i, ns := range namespaces {
			placeholders[i] = "?"
			args = append(args, ns)
		}
		conditions = append(conditions, "namespace IN ("+strings.Join(placeholders, ",")+")")
	}

	// Type filter
	if len(types) > 0 {
		placeholders := make([]string, len(types))
		for i, t := range types {
			placeholders[i] = "?"
			args = append(args, t)
		}
		conditions = append(conditions, "type IN ("+strings.Join(placeholders, ",")+")")
	}

	// Query filter (substring match)
	if query != "" {
		conditions = append(conditions, "(key LIKE ? OR content LIKE ? OR tags LIKE ?)")
		q := "%" + query + "%"
		args = append(args, q, q, q)
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	args = append(args, limit)
	rows, err := d.conn.Query(
		fmt.Sprintf(`SELECT id, namespace, key, content, type, tags, created_at, updated_at FROM memories %s ORDER BY updated_at DESC LIMIT ?`, where),
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var m Memory
		var tagsJSON string
		if err := rows.Scan(&m.ID, &m.Namespace, &m.Key, &m.Content, &m.Type, &tagsJSON, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(tagsJSON), &m.Tags)
		memories = append(memories, m)
	}
	return memories, nil
}

/*
 * ListMemories returns all memories in a namespace.
 * desc: Convenience wrapper around SearchMemories that filters by a single namespace with no query or type filter
 * param: namespace - the namespace to list memories from
 * param: limit - maximum number of results to return (defaults to 50 if <= 0)
 * return: slice of Memories and nil error, or nil and an error on query failure
 */
func (d *DB) ListMemories(namespace string, limit int) ([]Memory, error) {
	return d.SearchMemories([]string{namespace}, "", nil, limit)
}

/*
 * DeleteMemory removes a memory by ID.
 * desc: Deletes the memory row matching the given ID
 * param: id - the memory ID to delete
 * return: error if the memory is not found or the query fails, nil on success
 */
func (d *DB) DeleteMemory(id string) error {
	result, err := d.conn.Exec(`DELETE FROM memories WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("db: memory %q not found", id)
	}
	return nil
}

/*
 * scannable is a helper interface for row scanning.
 * desc: Abstraction over sql.Row and sql.Rows to allow shared scan logic
 */
type scannable interface {
	Scan(dest ...any) error
}

/*
 * scanMemory reads a memory row from a scannable source.
 * desc: Scans a SQL row into a Memory struct, deserializing the tags JSON column
 * param: row - any scannable source (sql.Row or sql.Rows)
 * return: pointer to the Memory and nil error, or nil and an error on scan failure
 */
func scanMemory(row scannable) (*Memory, error) {
	var m Memory
	var tagsJSON string
	if err := row.Scan(&m.ID, &m.Namespace, &m.Key, &m.Content, &m.Type, &tagsJSON, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(tagsJSON), &m.Tags)
	return &m, nil
}
