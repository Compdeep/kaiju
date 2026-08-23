package db

import "testing"

// Compaction used to delete what it summarised. What was lost was not the
// model's view of the conversation — that is the point of a summary — but the
// record: whatever the summarising call judged unimportant could not afterwards
// be read by the user or found by anything. Messages are now marked instead.

func seedConversation(t *testing.T, d *DB, sessionID string, n int) {
	t.Helper()
	if err := d.CreateSession(sessionID, "web", "u1", "t"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if err := d.AddMessage(sessionID, role, string(rune('a'+i%26))+"-message"); err != nil {
			t.Fatalf("add message: %v", err)
		}
	}
}

func TestMarkCompacted_KeepsEveryMessage(t *testing.T) {
	d := openTestDB(t)
	seedConversation(t, d, "s1", 30)

	summaryID, err := d.PrependMessage("s1", "system", "[Conversation summary]: earlier talk", 1)
	if err != nil {
		t.Fatalf("prepend summary: %v", err)
	}
	if err := d.MarkCompacted("s1", 10, summaryID); err != nil {
		t.Fatalf("mark: %v", err)
	}

	full, err := d.GetFullTranscript("s1", 0, 0)
	if err != nil {
		t.Fatalf("full transcript: %v", err)
	}
	if len(full) != 31 {
		t.Fatalf("nothing may be removed: 30 messages plus a summary is 31, got %d", len(full))
	}

	var compacted, live int
	for _, m := range full {
		if m.CompactedInto == summaryID {
			compacted++
		} else if m.CompactedInto == 0 {
			live++
		}
	}
	if compacted != 20 {
		t.Fatalf("20 of the 30 should be stood for by the summary, got %d", compacted)
	}
	if live != 11 {
		t.Fatalf("10 recent messages plus the summary stay live, got %d", live)
	}
}

// THE TRAP. The summary is written with a timestamp older than the messages it
// precedes, so it is among the oldest — and would be marked as standing for
// itself, disappearing from the very conversation it summarises.
func TestMarkCompacted_TheSummaryIsNotCompactedIntoItself(t *testing.T) {
	d := openTestDB(t)
	seedConversation(t, d, "s1", 30)

	summaryID, _ := d.PrependMessage("s1", "system", "[Conversation summary]: earlier talk", 1)
	if err := d.MarkCompacted("s1", 10, summaryID); err != nil {
		t.Fatalf("mark: %v", err)
	}

	for _, m := range mustFull(t, d, "s1") {
		if m.ID == summaryID && m.CompactedInto != 0 {
			t.Fatalf("the summary must stay live, got compacted_into=%d", m.CompactedInto)
		}
	}
	// And it must still be loaded for a model, or the run loses the summary too.
	for _, m := range mustRecent(t, d, "s1") {
		if m.ID == summaryID {
			return
		}
	}
	t.Fatal("the summary must be among the messages a model is given")
}

// The whole purpose: what is compacted stops reaching a model. A missed filter
// here sends an entire session into a prompt.
func TestCompactedMessagesAreNotLoadedForAModel(t *testing.T) {
	d := openTestDB(t)
	seedConversation(t, d, "s1", 30)
	summaryID, _ := d.PrependMessage("s1", "system", "[Conversation summary]: earlier talk", 1)
	if err := d.MarkCompacted("s1", 10, summaryID); err != nil {
		t.Fatalf("mark: %v", err)
	}

	for name, got := range map[string][]Message{
		"GetMessages":       mustAll(t, d, "s1"),
		"GetRecentMessages": mustRecent(t, d, "s1"),
	} {
		if len(got) != 11 {
			t.Errorf("%s returned %d messages; only the 10 live ones and the summary may reach a model", name, len(got))
		}
		for _, m := range got {
			if m.CompactedInto != 0 {
				t.Errorf("%s returned a compacted message (id %d)", name, m.ID)
			}
		}
	}
}

// Compacting twice must not reassign messages to the later summary, or the
// record of which summary stands for what becomes wrong.
func TestMarkCompacted_DoesNotReassignWhatIsAlreadyCompacted(t *testing.T) {
	d := openTestDB(t)
	seedConversation(t, d, "s1", 30)
	first, _ := d.PrependMessage("s1", "system", "[Conversation summary]: first", 1)
	if err := d.MarkCompacted("s1", 10, first); err != nil {
		t.Fatalf("first mark: %v", err)
	}

	seedMore(t, d, "s1", 20)
	second, _ := d.PrependMessage("s1", "system", "[Conversation summary]: second", 2)
	if err := d.MarkCompacted("s1", 10, second); err != nil {
		t.Fatalf("second mark: %v", err)
	}

	var toFirst int
	for _, m := range mustFull(t, d, "s1") {
		if m.CompactedInto == first {
			toFirst++
		}
	}
	if toFirst != 20 {
		t.Fatalf("the first summary must keep standing for its own 20 messages, got %d", toFirst)
	}
}

func seedMore(t *testing.T, d *DB, sessionID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := d.AddMessage(sessionID, "user", "later-message"); err != nil {
			t.Fatalf("add message: %v", err)
		}
	}
}

func mustFull(t *testing.T, d *DB, id string) []Message {
	t.Helper()
	m, err := d.GetFullTranscript(id, 0, 0)
	if err != nil {
		t.Fatalf("full transcript: %v", err)
	}
	return m
}

func mustAll(t *testing.T, d *DB, id string) []Message {
	t.Helper()
	m, err := d.GetMessages(id, 0)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	return m
}

func mustRecent(t *testing.T, d *DB, id string) []Message {
	t.Helper()
	m, err := d.GetRecentMessages(id, 0)
	if err != nil {
		t.Fatalf("recent messages: %v", err)
	}
	return m
}
