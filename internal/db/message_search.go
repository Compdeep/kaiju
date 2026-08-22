package db

import (
	"context"
	"database/sql"
	"strings"
	"unicode"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

/*
 * migrateMessageSearch builds the full-text index over messages.
 * desc: Creates the FTS5 table and the triggers that keep it in step with
 *       messages, then fills it if it is empty.
 * return: error if the index could not be built
 */
// migrateMessageSearch builds the full-text index over messages.
//
// The index is an external-content table: it holds the terms and reads the text
// from messages itself, so the transcript is not stored twice. The triggers
// carry every insert, update and delete across, which is what makes the index
// answer for the messages that exist rather than the ones that existed when it
// was built.
func (d *DB) migrateMessageSearch() error {
	// Whether the index is here already, asked before it is created. It cannot be
	// asked afterwards: for an external-content table a plain count reads the
	// messages table, so a brand new index with nothing in it reports as many rows
	// as there are messages and looks full.
	var built int
	if err := d.conn.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'messages_fts'`,
	).Scan(&built); err != nil {
		return err
	}

	stmts := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
			content,
			content='messages',
			content_rowid='id',
			tokenize='porter unicode61'
		)`,
		`CREATE TRIGGER IF NOT EXISTS messages_fts_insert AFTER INSERT ON messages BEGIN
			INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
		END`,
		`CREATE TRIGGER IF NOT EXISTS messages_fts_delete AFTER DELETE ON messages BEGIN
			INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
		END`,
		`CREATE TRIGGER IF NOT EXISTS messages_fts_update AFTER UPDATE ON messages BEGIN
			INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
			INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
		END;`,
	}
	for _, s := range stmts {
		if _, err := d.conn.Exec(s); err != nil {
			return err
		}
	}

	// Fill it, the once. The triggers only carry changes made after they exist, so
	// a database with messages already in it would otherwise have an index that
	// finds nothing older than this migration — a search answering "no" about
	// conversations that are sitting in the table.
	if built == 0 {
		if _, err := d.conn.Exec(`INSERT INTO messages_fts(messages_fts) VALUES ('rebuild')`); err != nil {
			return err
		}
	}
	return nil
}

/*
 * SearchMessages finds stored messages whose text matches a query.
 * desc: Runs a full-text search over every message, best match first, optionally
 *       confined to one session.
 * param: ctx - cancels the query
 * param: query - the words to look for
 * param: sessionID - one session, or empty for all of them
 * param: limit - most results to return
 * return: the matching messages, best match first
 */
// SearchMessages finds stored messages whose text matches a query.
//
// It searches the whole table, including messages a summary now stands for.
// Those are the ones a model can no longer see, so they are the ones a search is
// most often for; leaving them out would make the tool answer only about the
// part of the conversation that needs no searching.
func (d *DB) SearchMessages(ctx context.Context, query, sessionID string, limit int) ([]toolapi.FoundMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	found, err := d.searchMessages(ctx, query, sessionID, limit)
	if err == nil {
		return found, nil
	}
	// The query was not something the index could read. FTS5 has a syntax of its
	// own — quotes, NEAR, column filters — and text it cannot parse comes back as
	// an error, so an apostrophe or a stray bracket would fail the search rather
	// than find the words in it. Asked again for the bare words.
	plain := plainTerms(query)
	if plain == "" {
		// Nothing in the query to search for — punctuation only. No messages
		// contain it, which is an answer, where the index's parse error is not.
		return nil, nil
	}
	if plain == query {
		return nil, err
	}
	return d.searchMessages(ctx, plain, sessionID, limit)
}

func (d *DB) searchMessages(ctx context.Context, match, sessionID string, limit int) ([]toolapi.FoundMessage, error) {
	rows, err := d.conn.QueryContext(ctx,
		`SELECT m.id, m.session_id, COALESCE(s.title, ''), m.role, m.content, m.created_at
		   FROM messages_fts
		   JOIN messages m ON m.id = messages_fts.rowid
		   LEFT JOIN sessions s ON s.id = m.session_id
		  WHERE messages_fts MATCH ?
		    AND (? = '' OR m.session_id = ?)
		  ORDER BY bm25(messages_fts)
		  LIMIT ?`,
		match, sessionID, sessionID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var found []toolapi.FoundMessage
	for rows.Next() {
		var f toolapi.FoundMessage
		var title sql.NullString
		if err := rows.Scan(&f.ID, &f.SessionID, &title, &f.Role, &f.Content, &f.CreatedAt); err != nil {
			return nil, err
		}
		f.SessionTitle = title.String
		found = append(found, f)
	}
	return found, rows.Err()
}

// plainTerms reduces a query to its words, each quoted, all of them required.
//
// It is what a query falls back to when the index rejects it: every run of
// letters and digits becomes a phrase, and anything the index would have read as
// syntax is left out. A search for don't finds the messages saying don't rather
// than failing on the apostrophe.
func plainTerms(query string) string {
	var terms []string
	for _, w := range strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		// The index's own operators, which have to be capitals to be operators.
		// Left out rather than quoted: the query reached this fallback because the
		// index would not parse it, which usually means the caller was writing
		// syntax, and quoting AND would go looking for messages containing the word
		// "AND". A lower-case "not" or "or" is ordinary prose and is kept.
		switch w {
		case "AND", "OR", "NOT", "NEAR":
			continue
		}
		terms = append(terms, `"`+w+`"`)
	}
	return strings.Join(terms, " AND ")
}

var _ toolapi.MessageStore = (*DB)(nil)
