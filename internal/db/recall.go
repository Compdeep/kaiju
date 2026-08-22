package db

import (
	"context"
	"strings"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Defaults for a recall that asks for none.
const (
	recallHits     = 3
	recallAround   = 1
	recallMaxChars = 4000
)

/*
 * RecallMessages returns earlier messages mentioning any of the terms.
 * desc: Matches any term, keeps the best few, and returns them with the
 *       messages around them in the order they were written.
 * param: ctx - cancels the query
 * param: sessionID - the conversation to look in
 * param: terms - words to look for; a message matching any of them is a hit
 * param: opts - how far back to look and how much to return
 * return: the messages, oldest first
 */
func (d *DB) RecallMessages(ctx context.Context, sessionID string, terms []string, opts toolapi.Recall) ([]toolapi.FoundMessage, error) {
	match := anyOf(terms)
	if match == "" || sessionID == "" {
		return nil, nil
	}
	if opts.Hits <= 0 {
		opts.Hits = recallHits
	}
	if opts.Around < 0 {
		opts.Around = recallAround
	}
	if opts.MaxChars <= 0 {
		opts.MaxChars = recallMaxChars
	}

	// The conversation numbered, the matches found in it, then everything within
	// reach of a match — in one statement, because the messages around a match
	// cannot be asked for until the match is known, and asking separately would
	// be one round trip per hit.
	//
	// System messages are left out of the numbering entirely: the only ones here
	// are the summaries, which are already in the prompt this is filling, and
	// counting one as a neighbour would spend the budget on text the model has.
	rows, err := d.conn.QueryContext(ctx,
		`WITH ordered AS (
			SELECT id, role, content, created_at,
			       ROW_NUMBER() OVER (ORDER BY created_at, id) AS pos,
			       COUNT(*) OVER () AS total
			  FROM messages
			 WHERE session_id = ? AND role != 'system'
		),
		hits AS (
			SELECT o.pos AS pos
			  FROM messages_fts
			  JOIN ordered o ON o.id = messages_fts.rowid
			 WHERE messages_fts MATCH ?
			   AND o.pos <= o.total - ?
			 ORDER BY bm25(messages_fts)
			 LIMIT ?
		)
		SELECT o.id, o.role, o.content, o.created_at
		  FROM ordered o
		 WHERE EXISTS (SELECT 1 FROM hits h WHERE o.pos BETWEEN h.pos - ? AND h.pos + ?)
		 ORDER BY o.pos`,
		sessionID, match, opts.SkipNewest, opts.Hits, opts.Around, opts.Around,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var found []toolapi.FoundMessage
	for rows.Next() {
		var f toolapi.FoundMessage
		if err := rows.Scan(&f.ID, &f.Role, &f.Content, &f.CreatedAt); err != nil {
			return nil, err
		}
		f.SessionID = sessionID
		found = append(found, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return withinBudget(found, opts.MaxChars), nil
}

// withinBudget drops whole messages from the front until the rest fit.
//
// From the front because the newest of what was recalled is the likeliest to be
// what the question is about, and because a message cut in half reads as
// evidence of something it may not say.
func withinBudget(found []toolapi.FoundMessage, maxChars int) []toolapi.FoundMessage {
	total := 0
	for _, f := range found {
		total += len(f.Content)
	}
	for len(found) > 1 && total > maxChars {
		total -= len(found[0].Content)
		found = found[1:]
	}
	return found
}

// anyOf is the terms as one expression the index will match, any of them
// sufficing.
func anyOf(terms []string) string {
	var parts []string
	for _, t := range terms {
		if one := recallTerm(t); one != "" {
			parts = append(parts, one)
		}
	}
	return strings.Join(parts, " OR ")
}

// recallTerm is one term as the index will accept it.
func recallTerm(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return ""
	}
	if hasUnspaced(t) {
		return segmentForQuery(t)
	}
	// Quoted, so nothing inside it is read as the index's own syntax. The quote
	// character is the one thing that would end the quoting early, so it goes.
	cleaned := strings.ReplaceAll(t, `"`, "")
	if strings.TrimSpace(cleaned) == "" {
		return ""
	}
	return `"` + cleaned + `"`
}
