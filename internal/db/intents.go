package db

import "fmt"

/*
 * Intent is a configurable IGX level.
 * desc: Database-backed intent registry. Each intent has a sparse integer
 *       rank used by the gate for ordered comparison (lower rank = less
 *       privileged). Seed data comes from the config file; nothing is
 *       hardcoded in Go. Intents marked builtin cannot be deleted and
 *       their rank cannot be changed by the admin UI.
 */
type Intent struct {
	Name              string `json:"name"`
	Rank              int    `json:"rank"`
	Description       string `json:"description"`
	PromptDescription string `json:"prompt_description"`
	IsBuiltin         bool   `json:"is_builtin"`
	IsDefault         bool   `json:"is_default"`
}

/*
 * CreateIntent inserts a new intent row.
 * desc: Fails if the name is already taken.
 * param: i - the Intent to create.
 * return: error on failure.
 */
func (d *DB) CreateIntent(i Intent) error {
	builtin := 0
	if i.IsBuiltin {
		builtin = 1
	}
	def := 0
	if i.IsDefault {
		def = 1
	}
	_, err := d.conn.Exec(
		`INSERT INTO intents (name, rank, description, prompt_description, is_builtin, is_default) VALUES (?, ?, ?, ?, ?, ?)`,
		i.Name, i.Rank, i.Description, i.PromptDescription, builtin, def,
	)
	if err != nil {
		return fmt.Errorf("db: create intent: %w", err)
	}
	return nil
}

/*
 * GetIntent returns an intent by name.
 * param: name - the intent name.
 * return: pointer to the Intent or error.
 */
func (d *DB) GetIntent(name string) (*Intent, error) {
	row := d.conn.QueryRow(
		`SELECT name, rank, description, prompt_description, is_builtin, is_default FROM intents WHERE name = ?`,
		name,
	)
	var i Intent
	var builtin, def int
	if err := row.Scan(&i.Name, &i.Rank, &i.Description, &i.PromptDescription, &builtin, &def); err != nil {
		return nil, err
	}
	i.IsBuiltin = builtin == 1
	i.IsDefault = def == 1
	return &i, nil
}

/*
 * ListIntents returns all intents ordered by rank ascending.
 */
func (d *DB) ListIntents() ([]Intent, error) {
	rows, err := d.conn.Query(
		`SELECT name, rank, description, prompt_description, is_builtin, is_default FROM intents ORDER BY rank ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Intent
	for rows.Next() {
		var i Intent
		var builtin, def int
		if err := rows.Scan(&i.Name, &i.Rank, &i.Description, &i.PromptDescription, &builtin, &def); err != nil {
			return nil, err
		}
		i.IsBuiltin = builtin == 1
		i.IsDefault = def == 1
		out = append(out, i)
	}
	return out, nil
}

/*
 * UpdateIntent updates an existing intent.
 * desc: For builtins, rank cannot change — only descriptions may be edited.
 *       For custom intents, all fields are editable except name.
 * param: name - the intent name to update.
 * param: i - the new values.
 */
func (d *DB) UpdateIntent(name string, i Intent) error {
	existing, err := d.GetIntent(name)
	if err != nil {
		return fmt.Errorf("intent %q not found", name)
	}
	if existing.IsBuiltin {
		// Builtins: rank cannot be changed. Reject explicitly so the
		// caller sees an error instead of a silent no-op.
		if i.Rank != 0 && i.Rank != existing.Rank {
			return fmt.Errorf("intent %q is builtin: rank cannot be changed (current: %d)", name, existing.Rank)
		}
		_, err = d.conn.Exec(
			`UPDATE intents SET description = ?, prompt_description = ? WHERE name = ?`,
			i.Description, i.PromptDescription, name,
		)
	} else {
		_, err = d.conn.Exec(
			`UPDATE intents SET rank = ?, description = ?, prompt_description = ? WHERE name = ?`,
			i.Rank, i.Description, i.PromptDescription, name,
		)
	}
	return err
}

/*
 * DeleteIntent removes a custom intent. Builtins cannot be deleted.
 */
func (d *DB) DeleteIntent(name string) error {
	existing, err := d.GetIntent(name)
	if err != nil {
		return fmt.Errorf("intent %q not found", name)
	}
	if existing.IsBuiltin {
		return fmt.Errorf("intent %q is builtin and cannot be deleted", name)
	}
	// Cascade: remove any tool_intents pointing at this intent
	d.conn.Exec(`DELETE FROM tool_intents WHERE intent_name = ?`, name)
	_, err = d.conn.Exec(`DELETE FROM intents WHERE name = ?`, name)
	return err
}

/*
 * SeedIntentsFromConfig inserts intents declared in the config file on
 * first run. Uses INSERT OR IGNORE so edits made via the admin UI survive
 * restarts — config is a bootstrap hint, the DB is authoritative after
 * install. This is the ONLY seed path; Go code contains no default intent
 * definitions.
 * param: seeds - intents to seed (name, rank, description, prompt description, builtin flag)
 * return: error if insertion fails
 */
func (d *DB) SeedIntentsFromConfig(seeds []Intent) error {
	for _, i := range seeds {
		if i.Name == "" {
			continue
		}
		builtin := 0
		if i.IsBuiltin {
			builtin = 1
		}
		def := 0
		if i.IsDefault {
			def = 1
		}
		_, err := d.conn.Exec(
			`INSERT OR IGNORE INTO intents (name, rank, description, prompt_description, is_builtin, is_default) VALUES (?, ?, ?, ?, ?, ?)`,
			i.Name, i.Rank, i.Description, i.PromptDescription, builtin, def,
		)
		if err != nil {
			return fmt.Errorf("seed intent %q: %w", i.Name, err)
		}
	}
	return nil
}

// ── Tool intent overrides ──

/*
 * SetToolIntent assigns an intent to a tool. Overrides the tool's Go
 * Impact() default until removed.
 */
func (d *DB) SetToolIntent(toolName, intentName string) error {
	_, err := d.conn.Exec(
		`INSERT OR REPLACE INTO tool_intents (tool_name, intent_name) VALUES (?, ?)`,
		toolName, intentName,
	)
	return err
}

/*
 * GetToolIntent returns the configured intent name for a tool, or empty
 * string if no override exists (caller should fall back to tool.Impact()).
 */
func (d *DB) GetToolIntent(toolName string) (string, error) {
	row := d.conn.QueryRow(`SELECT intent_name FROM tool_intents WHERE tool_name = ?`, toolName)
	var name string
	if err := row.Scan(&name); err != nil {
		return "", err
	}
	return name, nil
}

/*
 * ListToolIntents returns all tool→intent overrides.
 */
func (d *DB) ListToolIntents() (map[string]string, error) {
	rows, err := d.conn.Query(`SELECT tool_name, intent_name FROM tool_intents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var tool, intent string
		if err := rows.Scan(&tool, &intent); err != nil {
			return nil, err
		}
		out[tool] = intent
	}
	return out, nil
}

/*
 * DeleteToolIntent removes a tool override (falls back to Go default).
 */
func (d *DB) DeleteToolIntent(toolName string) error {
	_, err := d.conn.Exec(`DELETE FROM tool_intents WHERE tool_name = ?`, toolName)
	return err
}
