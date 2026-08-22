package db

import (
	"context"
	"strings"
	"testing"
)

// newSearchDB gives a database with one session and the messages named.
func newSearchDB(t *testing.T, session string, texts ...string) *DB {
	t.Helper()
	d, err := Open(t.TempDir() + "/search.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.CreateSession(session, "test", "tester", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i, text := range texts {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if err := d.AddMessage(session, role, text); err != nil {
			t.Fatalf("add message %d: %v", i, err)
		}
	}
	return d
}

// A search finds a message by a word in it.
func TestSearchMessagesFindsAWord(t *testing.T) {
	d := newSearchDB(t, "s1",
		"how do I restart the web server",
		"edit the configuration and reload it",
		"what is the weather like",
	)
	found, err := d.SearchMessages(context.Background(), "configuration", "", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("want 1 match, got %d: %+v", len(found), found)
	}
	if !strings.Contains(found[0].Content, "configuration") {
		t.Errorf("matched the wrong message: %q", found[0].Content)
	}
	if found[0].SessionID != "s1" {
		t.Errorf("session id = %q, want s1", found[0].SessionID)
	}
	if found[0].Role != "assistant" {
		t.Errorf("role = %q, want assistant", found[0].Role)
	}
}

// The index stems, so a search for the singular finds the plural.
func TestSearchMessagesStems(t *testing.T) {
	d := newSearchDB(t, "s1", "list the running processes on that host")
	found, err := d.SearchMessages(context.Background(), "process", "", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("want the plural to match the singular, got %d", len(found))
	}
}

// A search can be confined to one conversation.
func TestSearchMessagesScopedToASession(t *testing.T) {
	d := newSearchDB(t, "s1", "the whale surfaced")
	if err := d.CreateSession("s2", "test", "tester", ""); err != nil {
		t.Fatalf("create second session: %v", err)
	}
	if err := d.AddMessage("s2", "user", "another whale entirely"); err != nil {
		t.Fatalf("add: %v", err)
	}

	all, err := d.SearchMessages(context.Background(), "whale", "", 10)
	if err != nil {
		t.Fatalf("search all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("across sessions: want 2, got %d", len(all))
	}
	one, err := d.SearchMessages(context.Background(), "whale", "s2", 10)
	if err != nil {
		t.Fatalf("search one: %v", err)
	}
	if len(one) != 1 || one[0].SessionID != "s2" {
		t.Fatalf("confined to s2: got %+v", one)
	}
}

// A message a summary stands for is still findable. That is the case the tool
// exists for: it is the part of the conversation a model can no longer see.
func TestSearchMessagesFindsCompactedMessages(t *testing.T) {
	d := newSearchDB(t, "s1",
		"the first thing we discussed was barnacles",
		"noted",
		"and then something else",
		"noted again",
	)
	summaryID, err := d.PrependMessage("s1", "system", "[Conversation summary]: we talked", 1)
	if err != nil {
		t.Fatalf("prepend: %v", err)
	}
	if err := d.MarkCompacted("s1", 2, summaryID); err != nil {
		t.Fatalf("mark: %v", err)
	}

	// Gone from what a model is sent...
	live, err := d.GetMessages("s1", 100)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	for _, m := range live {
		if strings.Contains(m.Content, "barnacles") {
			t.Fatal("the compacted message is still in the model's window")
		}
	}
	// ...and still findable.
	found, err := d.SearchMessages(context.Background(), "barnacles", "", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("want the compacted message found, got %d", len(found))
	}
}

// Punctuation the index would read as its own syntax does not fail the search:
// the words in the query are still found, and a query the index cannot parse
// comes back as no match rather than as an error.
func TestSearchMessagesSurvivesQuerySyntax(t *testing.T) {
	d := newSearchDB(t, "s1", "I said don't restart it")
	cases := []struct {
		query string
		want  int
	}{
		{`don't`, 1},
		{`restart)`, 1},
		{`restart AND (it`, 1},
		{`"unbalanced`, 0}, // parses once quoted, and the word is not there
		{`((()))`, 0},      // nothing left to search for at all
	}
	for _, c := range cases {
		found, err := d.SearchMessages(context.Background(), c.query, "", 10)
		if err != nil {
			t.Errorf("query %q: %v", c.query, err)
			continue
		}
		if len(found) != c.want {
			t.Errorf("query %q found %d, want %d", c.query, len(found), c.want)
		}
	}
}

// Editing a message changes what a search finds, and deleting it removes it.
func TestSearchMessagesTracksChanges(t *testing.T) {
	d := newSearchDB(t, "s1", "the original wording mentions kestrels")
	live, _ := d.GetMessages("s1", 100)
	id := live[0].ID

	if _, err := d.conn.Exec(`UPDATE messages SET content = ? WHERE id = ?`, "now it mentions ospreys", id); err != nil {
		t.Fatalf("update: %v", err)
	}
	if found, _ := d.SearchMessages(context.Background(), "kestrels", "", 10); len(found) != 0 {
		t.Error("the old wording is still indexed after an edit")
	}
	if found, _ := d.SearchMessages(context.Background(), "ospreys", "", 10); len(found) != 1 {
		t.Error("the new wording was not indexed after an edit")
	}

	if _, err := d.conn.Exec(`DELETE FROM messages WHERE id = ?`, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if found, _ := d.SearchMessages(context.Background(), "ospreys", "", 10); len(found) != 0 {
		t.Error("a deleted message is still indexed")
	}
}

// Messages already stored when the index is built are indexed, not only the
// ones written afterwards.
func TestSearchMessagesIndexesWhatWasAlreadyThere(t *testing.T) {
	d := newSearchDB(t, "s1", "an older conversation about sextants")

	// Drop the index and its triggers, then migrate again — the state of a
	// database that predates this index.
	for _, s := range []string{
		`DROP TRIGGER messages_fts_insert`, `DROP TRIGGER messages_fts_delete`,
		`DROP TRIGGER messages_fts_update`, `DROP TABLE messages_fts`,
	} {
		if _, err := d.conn.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := d.migrateMessageSearch(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	found, err := d.SearchMessages(context.Background(), "sextants", "", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("want the pre-existing message indexed, got %d", len(found))
	}
}
