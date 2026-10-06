package db

import (
	"strings"
	"testing"
)

// A reader opening a conversation sees where it got to, not where it started.
//
// This was ORDER BY created_at LIMIT, which returns the OLDEST rows. A session
// past the limit served its opening and never its present: a 257-message
// conversation showed everything up to its 200th message and nothing after. The
// day's work was not missing from the database, it was never asked for.
//
// It also broke something that only looks unrelated. The view decided a query was
// still running by testing whether the last message it held was a user one, and
// the 200th message of that conversation happened to be a question. So it showed
// a stop button for a run that had finished hours earlier.

func TestFullTranscript_ReturnsTheEndOfTheConversation(t *testing.T) {
	d := openTestDB(t)
	seedConversation(t, d, "s1", 257)

	msgs, err := d.GetFullTranscript("s1", 200, 0)
	if err != nil {
		t.Fatalf("transcript: %v", err)
	}
	if len(msgs) != 200 {
		t.Fatalf("got %d messages, want the 200 asked for", len(msgs))
	}

	// The newest message must be in there. Under the old query it was row 257 of
	// 257 and the window stopped at 200.
	all, err := d.GetFullTranscript("s1", 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	newest := all[len(all)-1]
	if msgs[len(msgs)-1].ID != newest.ID {
		t.Errorf("the page ends at message %d, but the conversation ends at %d — "+
			"the reader is being shown the start", msgs[len(msgs)-1].ID, newest.ID)
	}
	// And the oldest must be the one it cut, not the one it kept.
	if msgs[0].ID == all[0].ID {
		t.Error("the page begins at the first message ever sent, so nothing was cut from the old end")
	}
}

// Reading order, because that is how a conversation is read. The query takes the
// newest first so the limit cuts the right end, and has to undo that.
func TestFullTranscript_IsOldestFirstWithinThePage(t *testing.T) {
	d := openTestDB(t)
	seedConversation(t, d, "s1", 40)

	msgs, err := d.GetFullTranscript("s1", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 10 {
		t.Fatalf("got %d, want 10", len(msgs))
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].ID < msgs[i-1].ID {
			t.Fatalf("message %d (id %d) comes after id %d; the page is in reverse",
				i, msgs[i].ID, msgs[i-1].ID)
		}
	}
}

// offset pages backwards from the end, which is the direction a reader scrolling
// up asks in. Consecutive pages must not overlap and must not skip.
func TestFullTranscript_OffsetPagesBackwards(t *testing.T) {
	d := openTestDB(t)
	seedConversation(t, d, "s1", 40)

	recent, err := d.GetFullTranscript("s1", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	earlier, err := d.GetFullTranscript("s1", 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(earlier) != 10 {
		t.Fatalf("second page has %d messages", len(earlier))
	}
	if earlier[len(earlier)-1].ID >= recent[0].ID {
		t.Errorf("page 2 ends at id %d and page 1 starts at id %d — the pages overlap",
			earlier[len(earlier)-1].ID, recent[0].ID)
	}
	if earlier[len(earlier)-1].ID != recent[0].ID-1 {
		t.Errorf("page 2 ends at %d and page 1 starts at %d — a message between them is in neither",
			earlier[len(earlier)-1].ID, recent[0].ID)
	}
}

// A session shorter than the limit is returned whole, unreversed and unpaged.
func TestFullTranscript_AShortSessionIsWholeAndInOrder(t *testing.T) {
	d := openTestDB(t)
	seedConversation(t, d, "s1", 5)

	msgs, err := d.GetFullTranscript("s1", 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 {
		t.Fatalf("got %d, want all 5", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "a-message") {
		t.Errorf("first message is %q, want the first one sent", msgs[0].Content)
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].ID < msgs[i-1].ID {
			t.Fatal("a short session came back reversed")
		}
	}
}

// Compacted messages are still part of the record a person reads, so the tail
// must carry them with their compacted_into intact.
func TestFullTranscript_CarriesCompactedMessages(t *testing.T) {
	d := openTestDB(t)
	seedConversation(t, d, "s1", 12)
	sum, err := d.PrependMessage("s1", "system", "[Conversation summary]: earlier talk", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.MarkCompacted("s1", 4, sum); err != nil {
		t.Fatal(err)
	}

	msgs, err := d.GetFullTranscript("s1", 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	var folded int
	for _, m := range msgs {
		if m.CompactedInto == sum {
			folded++
		}
	}
	if folded == 0 {
		t.Error("no message points at the summary, so the view cannot fold anything under it")
	}
}
