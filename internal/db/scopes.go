package db

import (
	"encoding/json"
	"fmt"
	"github.com/Compdeep/kaiju/agent/toolapi"
	"github.com/Compdeep/kaiju/permissions"
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
	// IntentCap is how far a run may go for someone holding this scope, on the same
	// scale as a tool's impact: 0 observe, 100 affect, 200 control. A person's own
	// ceiling still applies on top, and the lower of the two wins.
	IntentCap int `json:"intentCap"`
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
		`INSERT INTO scopes (name, description, tools, cap, intent_cap) VALUES (?, ?, ?, ?, ?)`,
		s.Name, s.Description, string(toolsJSON), string(capJSON), s.IntentCap,
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
	row := d.conn.QueryRow(`SELECT name, description, tools, cap, intent_cap FROM scopes WHERE name = ?`, name)
	var s Scope
	var toolsJSON, capJSON string
	if err := row.Scan(&s.Name, &s.Description, &toolsJSON, &capJSON, &s.IntentCap); err != nil {
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
	rows, err := d.conn.Query(`SELECT name, description, tools, cap, intent_cap FROM scopes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var scopes []Scope
	for rows.Next() {
		var s Scope
		var toolsJSON, capJSON string
		if err := rows.Scan(&s.Name, &s.Description, &toolsJSON, &capJSON, &s.IntentCap); err != nil {
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
		`UPDATE scopes SET description = ?, tools = ?, cap = ?, intent_cap = ? WHERE name = ?`,
		s.Description, string(toolsJSON), string(capJSON), s.IntentCap, name,
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
 * ResolveUserScope works out what a user may do.
 *
 * The rule itself lives in the permissions package, where an application embedding this
 * one can reach it. It was written here as well, and the two copies came to disagree:
 * this one never read a user's groups, so somebody put in a group holding permissions
 * received none of them, while the application's copy granted them. Neither program
 * could see the other's version, so nothing could report the difference.
 *
 * This now reads the rows and hands the values over. Every scope and group is fetched
 * rather than the few a user names, because there are a handful of each and one query
 * apiece is simpler to read than a loop of lookups.
 *
 * param: user - the user to resolve.
 * return: what they may do, or the error from reading the rows.
 */
func (d *DB) ResolveUserScope(user *User) (*UserScopeResult, error) {
	allScopes, err := d.ListScopes()
	if err != nil {
		return nil, fmt.Errorf("db: resolve scope for %q: %w", user.Username, err)
	}
	allGroups, err := d.ListGroups()
	if err != nil {
		return nil, fmt.Errorf("db: resolve scope for %q: %w", user.Username, err)
	}

	scopes := make(map[string]permissions.Scope, len(allScopes))
	for _, s := range allScopes {
		scopes[s.Name] = permissions.Scope{
			Name: s.Name, Tools: s.Tools, Cap: s.Cap, IntentCap: s.IntentCap,
		}
	}
	groups := make(map[string]permissions.Group, len(allGroups))
	for _, g := range allGroups {
		groups[g.Name] = permissions.Group{Name: g.Name, Scopes: g.Scopes}
	}

	answer := permissions.Resolve(permissions.User{
		Username:  user.Username,
		Scopes:    user.Scopes,
		Groups:    user.Groups,
		MaxIntent: user.MaxIntent,
	}, scopes, groups)

	return &UserScopeResult{
		Username:     answer.Username,
		AllowedTools: answer.AllowedTools,
		MaxImpact:    answer.MaxImpact,
		MaxIntent:    answer.MaxIntent,
	}, nil
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
			IntentCap:   toolapi.ImpactControl,
		},
		{
			Name:        "standard",
			Description: "All tools with destructive operations capped",
			Tools:       []string{"*"},
			Cap:         map[string]int{"bash": 100, "git": 100},
			IntentCap:   toolapi.ImpactControl,
		},
		{
			Name:        "readonly",
			Description: "Read-only — no side effects, no writes",
			Tools: []string{
				"sysinfo", "file_read", "file_list", "web_search", "web_fetch",
				"process_list", "net_info", "env_list", "disk_usage",
				"memory_recall", "memory_search", "clipboard",
			},
			// Read-only means read-only: this scope reaches nothing that changes
			// anything, whatever tools a later edit adds to the list above.
			IntentCap: toolapi.ImpactObserve,
		},
	}

	for _, s := range defaults {
		toolsJSON, _ := json.Marshal(s.Tools)
		capJSON, _ := json.Marshal(s.Cap)
		d.conn.Exec(
			`INSERT OR IGNORE INTO scopes (name, description, tools, cap, intent_cap) VALUES (?, ?, ?, ?, ?)`,
			s.Name, s.Description, string(toolsJSON), string(capJSON), s.IntentCap,
		)
	}
	return nil
}
