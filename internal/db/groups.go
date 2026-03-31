package db

import (
	"encoding/json"
	"fmt"
)

/*
 * Group is a collection of scopes that can be assigned to users.
 * desc: Named bundle of scope references, stored in the groups table with scopes serialized as JSON in the profiles column
 */
type Group struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Scopes      []string `json:"scopes"`
}

/*
 * CreateGroup adds a new group.
 * desc: Inserts a group row with serialized scopes JSON
 * param: g - the Group to create (Name, Description, and Scopes fields are used)
 * return: error if insertion fails, nil on success
 */
func (d *DB) CreateGroup(g Group) error {
	scopesJSON, _ := json.Marshal(g.Scopes)
	_, err := d.conn.Exec(
		`INSERT INTO groups (name, description, profiles) VALUES (?, ?, ?)`,
		g.Name, g.Description, string(scopesJSON),
	)
	if err != nil {
		return fmt.Errorf("db: create group: %w", err)
	}
	return nil
}

/*
 * GetGroup returns a group by name.
 * desc: Looks up a single group row by primary key and deserializes its scopes JSON
 * param: name - the group name to look up
 * return: pointer to the Group and nil error, or nil and an error if not found
 */
func (d *DB) GetGroup(name string) (*Group, error) {
	row := d.conn.QueryRow(`SELECT name, description, profiles FROM groups WHERE name = ?`, name)
	var g Group
	var scopesJSON string
	if err := row.Scan(&g.Name, &g.Description, &scopesJSON); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(scopesJSON), &g.Scopes)
	return &g, nil
}

/*
 * ListGroups returns all groups.
 * desc: Queries all group rows ordered by name
 * return: slice of all Groups and nil error, or nil and an error on query failure
 */
func (d *DB) ListGroups() ([]Group, error) {
	rows, err := d.conn.Query(`SELECT name, description, profiles FROM groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []Group
	for rows.Next() {
		var g Group
		var scopesJSON string
		if err := rows.Scan(&g.Name, &g.Description, &scopesJSON); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(scopesJSON), &g.Scopes)
		groups = append(groups, g)
	}
	return groups, nil
}

/*
 * UpdateGroup updates an existing group.
 * desc: Replaces the description and scopes for the group identified by name
 * param: name - the group name to update (used as the WHERE key)
 * param: g - Group struct containing the new Description and Scopes values
 * return: error if the group is not found or the query fails, nil on success
 */
func (d *DB) UpdateGroup(name string, g Group) error {
	scopesJSON, _ := json.Marshal(g.Scopes)
	result, err := d.conn.Exec(
		`UPDATE groups SET description = ?, profiles = ? WHERE name = ?`,
		g.Description, string(scopesJSON), name,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("db: group %q not found", name)
	}
	return nil
}

/*
 * DeleteGroup removes a group.
 * desc: Deletes the group row matching the given name
 * param: name - the group name to delete
 * return: error if the group is not found or the query fails, nil on success
 */
func (d *DB) DeleteGroup(name string) error {
	result, err := d.conn.Exec(`DELETE FROM groups WHERE name = ?`, name)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("db: group %q not found", name)
	}
	return nil
}
