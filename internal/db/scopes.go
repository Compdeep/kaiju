package db

import (
	"encoding/json"
	"fmt"
)

/*
 * Scope defines a named set of tool permissions.
 * desc: Default-deny permission set where Tools lists allowed tool names (or ["*"] for all) and Cap limits per-tool impact
 */
type Scope struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Tools       []string       `json:"tools"`
	Cap         map[string]int `json:"cap,omitempty"`
}

/*
 * CreateScope adds a new scope.
 * desc: Inserts a scope row with serialized tools and cap JSON
 * param: s - the Scope to create (Name, Description, Tools, and Cap fields are used)
 * return: error if insertion fails, nil on success
 */
func (d *DB) CreateScope(s Scope) error {
	toolsJSON, _ := json.Marshal(s.Tools)
	capJSON, _ := json.Marshal(s.Cap)
	_, err := d.conn.Exec(
		`INSERT INTO scopes (name, description, tools, cap) VALUES (?, ?, ?, ?)`,
		s.Name, s.Description, string(toolsJSON), string(capJSON),
	)
	if err != nil {
		return fmt.Errorf("db: create scope: %w", err)
	}
	return nil
}

/*
 * GetScope returns a scope by name.
 * desc: Looks up a single scope row by primary key and deserializes its JSON fields
 * param: name - the scope name to look up
 * return: pointer to the Scope and nil error, or nil and an error if not found
 */
func (d *DB) GetScope(name string) (*Scope, error) {
	row := d.conn.QueryRow(`SELECT name, description, tools, cap FROM scopes WHERE name = ?`, name)
	var s Scope
	var toolsJSON, capJSON string
	if err := row.Scan(&s.Name, &s.Description, &toolsJSON, &capJSON); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(toolsJSON), &s.Tools)
	json.Unmarshal([]byte(capJSON), &s.Cap)
	if s.Cap == nil {
		s.Cap = make(map[string]int)
	}
	return &s, nil
}

/*
 * ListScopes returns all scopes.
 * desc: Queries all scope rows ordered by name
 * return: slice of all Scopes and nil error, or nil and an error on query failure
 */
func (d *DB) ListScopes() ([]Scope, error) {
	rows, err := d.conn.Query(`SELECT name, description, tools, cap FROM scopes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var scopes []Scope
	for rows.Next() {
		var s Scope
		var toolsJSON, capJSON string
		if err := rows.Scan(&s.Name, &s.Description, &toolsJSON, &capJSON); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(toolsJSON), &s.Tools)
		json.Unmarshal([]byte(capJSON), &s.Cap)
		if s.Cap == nil {
			s.Cap = make(map[string]int)
		}
		scopes = append(scopes, s)
	}
	return scopes, nil
}

/*
 * UpdateScope updates an existing scope.
 * desc: Replaces the description, tools, and cap for the scope identified by name
 * param: name - the scope name to update (used as the WHERE key)
 * param: s - Scope struct containing the new Description, Tools, and Cap values
 * return: error if the scope is not found or the query fails, nil on success
 */
func (d *DB) UpdateScope(name string, s Scope) error {
	toolsJSON, _ := json.Marshal(s.Tools)
	capJSON, _ := json.Marshal(s.Cap)
	result, err := d.conn.Exec(
		`UPDATE scopes SET description = ?, tools = ?, cap = ? WHERE name = ?`,
		s.Description, string(toolsJSON), string(capJSON), name,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("db: scope %q not found", name)
	}
	return nil
}

/*
 * DeleteScope removes a scope.
 * desc: Deletes the scope row matching the given name
 * param: name - the scope name to delete
 * return: error if the scope is not found or the query fails, nil on success
 */
func (d *DB) DeleteScope(name string) error {
	result, err := d.conn.Exec(`DELETE FROM scopes WHERE name = ?`, name)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("db: scope %q not found", name)
	}
	return nil
}

/*
 * UserScopeResult is the merged tool permission set for a user.
 * desc: Aggregated result from resolving all of a user's scopes into a single allow/cap/intent set, converted to agent.ResolvedScope by the API layer
 */
type UserScopeResult struct {
	Username     string
	AllowedTools map[string]bool // tool name -> allowed. "*" key means all tools.
	MaxImpact    map[string]int  // tool name -> per-tool impact cap
	MaxIntent    int
}

/*
 * ResolveUserScope merges all scopes assigned to a user into a single permission set.
 * desc: Unions allowed tools across scopes, takes the strictest per-tool cap, and caps intent at the user's max_intent. Returns deny-all if user has no scopes.
 * param: user - the User whose scopes should be resolved
 * return: merged UserScopeResult and nil error, or nil and an error on scope lookup failure
 */
func (d *DB) ResolveUserScope(user *User) (*UserScopeResult, error) {
	if len(user.Scopes) == 0 {
		return &UserScopeResult{
			Username:     user.Username,
			AllowedTools: make(map[string]bool),
			MaxImpact:    make(map[string]int),
			MaxIntent:    0,
		}, nil
	}

	resolved := &UserScopeResult{
		Username:     user.Username,
		AllowedTools: make(map[string]bool),
		MaxImpact:    make(map[string]int),
		MaxIntent:    2, // start at max, take min
	}

	for _, scopeName := range user.Scopes {
		scope, err := d.GetScope(scopeName)
		if err != nil {
			continue // scope not found, skip
		}

		// Tools: union
		for _, tool := range scope.Tools {
			resolved.AllowedTools[tool] = true
		}

		// Caps: strictest wins per tool
		for tool, cap := range scope.Cap {
			if existing, ok := resolved.MaxImpact[tool]; ok {
				if cap < existing {
					resolved.MaxImpact[tool] = cap
				}
			} else {
				resolved.MaxImpact[tool] = cap
			}
		}
	}

	// Cap by user's own max_intent
	if user.MaxIntent < resolved.MaxIntent {
		resolved.MaxIntent = user.MaxIntent
	}

	return resolved, nil
}

/*
 * SeedDefaultScopes creates the built-in scopes if they don't exist.
 * desc: Inserts admin, standard, and readonly scopes using INSERT OR IGNORE so existing rows are preserved
 * return: nil (always succeeds; individual insert errors are ignored)
 */
func (d *DB) SeedDefaultScopes() error {
	defaults := []Scope{
		{
			Name:        "admin",
			Description: "Full unrestricted access — all tools, no caps",
			Tools:       []string{"*"},
		},
		{
			Name:        "standard",
			Description: "All tools with destructive operations capped",
			Tools:       []string{"*"},
			Cap:         map[string]int{"bash": 1, "git": 1},
		},
		{
			Name:        "readonly",
			Description: "Read-only — no side effects, no writes",
			Tools: []string{
				"sysinfo", "file_read", "file_list", "web_search", "web_fetch",
				"process_list", "net_info", "env_list", "disk_usage",
				"memory_recall", "memory_search", "clipboard",
			},
		},
	}

	for _, s := range defaults {
		toolsJSON, _ := json.Marshal(s.Tools)
		capJSON, _ := json.Marshal(s.Cap)
		d.conn.Exec(
			`INSERT OR IGNORE INTO scopes (name, description, tools, cap) VALUES (?, ?, ?, ?)`,
			s.Name, s.Description, string(toolsJSON), string(capJSON),
		)
	}
	return nil
}
