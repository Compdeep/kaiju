package db

import (
	"encoding/json"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

/*
 * User represents a registered user.
 * desc: Holds account info including credentials, permission scopes, group memberships, and status
 */
type User struct {
	Username  string   `json:"username"`
	PassHash  string   `json:"-"`
	MaxIntent int      `json:"max_intent"`
	Scopes    []string `json:"scopes"`
	Groups    []string `json:"groups"`
	Disabled  bool     `json:"disabled"`
	CreatedAt int64    `json:"created_at"`
}

/*
 * UserCount returns the number of users in the database.
 * desc: Counts all rows in the users table
 * return: total user count and nil error, or zero and an error on failure
 */
func (d *DB) UserCount() (int, error) {
	var count int
	err := d.conn.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

/*
 * CreateUser adds a new user with a bcrypt-hashed password.
 * desc: Inserts a user row with hashed password, intent level, and scope assignments
 * param: username - unique login name for the user
 * param: password - plaintext password that will be bcrypt-hashed before storage
 * param: maxIntent - maximum intent level allowed for this user
 * param: scopes - list of scope names assigned to this user
 * return: error if hashing or insertion fails, nil on success
 */
func (d *DB) CreateUser(username, password string, maxIntent int, scopes []string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("db: hash password: %w", err)
	}
	scopesJSON, _ := json.Marshal(scopes)
	_, err = d.conn.Exec(
		`INSERT INTO users (username, pass_hash, max_intent, scopes, profiles, groups, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		username, string(hash), maxIntent, string(scopesJSON), "[]", "[]", time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("db: create user: %w", err)
	}
	return nil
}

/*
 * scanUser reads a user row. Reads the legacy profiles column but discards it.
 * desc: Scans a SQL row into a User struct, deserializing JSON columns for scopes and groups
 * param: scan - scanner function (typically row.Scan or rows.Scan) to read column values
 * return: populated User and nil error, or zero-value User and an error on scan failure
 */
func scanUser(scan func(dest ...any) error) (User, error) {
	var u User
	var scopesJSON, profilesJSON, groupsJSON string
	if err := scan(&u.Username, &u.PassHash, &u.MaxIntent, &scopesJSON, &profilesJSON, &groupsJSON, &u.Disabled, &u.CreatedAt); err != nil {
		return u, err
	}
	json.Unmarshal([]byte(scopesJSON), &u.Scopes)
	json.Unmarshal([]byte(groupsJSON), &u.Groups)
	// profilesJSON intentionally discarded — legacy field
	return u, nil
}

/*
 * GetUser returns a user by username.
 * desc: Looks up a single user row by primary key
 * param: username - the username to look up
 * return: pointer to the User and nil error, or nil and an error if not found
 */
func (d *DB) GetUser(username string) (*User, error) {
	row := d.conn.QueryRow(
		`SELECT username, pass_hash, max_intent, scopes, profiles, groups, disabled, created_at FROM users WHERE username = ?`,
		username,
	)
	u, err := scanUser(row.Scan)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

/*
 * AuthenticateUser verifies credentials and returns the user.
 * desc: Fetches the user, checks the account is not disabled, and validates the bcrypt password
 * param: username - the username to authenticate
 * param: password - the plaintext password to verify against the stored hash
 * return: pointer to the authenticated User and nil error, or nil and an error on invalid credentials or disabled account
 */
func (d *DB) AuthenticateUser(username, password string) (*User, error) {
	u, err := d.GetUser(username)
	if err != nil {
		return nil, fmt.Errorf("db: invalid credentials")
	}
	if u.Disabled {
		return nil, fmt.Errorf("db: user %q is disabled", username)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PassHash), []byte(password)); err != nil {
		return nil, fmt.Errorf("db: invalid credentials")
	}
	return u, nil
}

/*
 * ListUsers returns all users.
 * desc: Queries all user rows ordered by creation time
 * return: slice of all Users and nil error, or nil and an error on query failure
 */
func (d *DB) ListUsers() ([]User, error) {
	rows, err := d.conn.Query(
		`SELECT username, pass_hash, max_intent, scopes, profiles, groups, disabled, created_at FROM users ORDER BY created_at`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		u, err := scanUser(rows.Scan)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, nil
}

/*
 * DeleteUser removes a user.
 * desc: Deletes the user row matching the given username
 * param: username - the username to delete
 * return: error if the user is not found or the query fails, nil on success
 */
func (d *DB) DeleteUser(username string) error {
	result, err := d.conn.Exec(`DELETE FROM users WHERE username = ?`, username)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("db: user %q not found", username)
	}
	return nil
}

/*
 * UpdateUser updates a user's settings.
 * desc: Sets the max intent, scopes, groups, and disabled flag for an existing user
 * param: username - the user to update
 * param: maxIntent - new maximum intent level
 * param: scopes - new list of scope names
 * param: groups - new list of group names
 * param: disabled - whether the account should be disabled
 * return: error on query failure, nil on success
 */
func (d *DB) UpdateUser(username string, maxIntent int, scopes, groups []string, disabled bool) error {
	scopesJSON, _ := json.Marshal(scopes)
	groupsJSON, _ := json.Marshal(groups)
	_, err := d.conn.Exec(
		`UPDATE users SET max_intent = ?, scopes = ?, groups = ?, disabled = ? WHERE username = ?`,
		maxIntent, string(scopesJSON), string(groupsJSON), disabled, username,
	)
	return err
}
