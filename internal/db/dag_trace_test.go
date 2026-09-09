package db

import (
	"errors"
	"testing"
)

// A trace belongs to one message. It used to be saved against "the newest
// assistant message in this session":
//
//	UPDATE ... WHERE id = (SELECT id ... ORDER BY created_at DESC LIMIT 1)
//
// which is a different message as soon as anything else answers. Two writers
// raced for that row — the run saved its own snapshot, the browser posted the
// nodes it had watched — and the last to arrive won, wherever it landed. A
// one-node interjection replaced the trace of the run that had done the work.

func seedTwoAnswers(t *testing.T, d *DB) (first, second int64) {
	t.Helper()
	if err := d.CreateSession("s1", "web", "u1", "t"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	var err error
	if first, err = d.AddMessage("s1", "assistant", "first answer"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if second, err = d.AddMessage("s1", "assistant", "second answer"); err != nil {
		t.Fatalf("second: %v", err)
	}
	return first, second
}

func traceOf(t *testing.T, d *DB, sessionID string, id int64) string {
	t.Helper()
	msgs, err := d.GetMessages(sessionID, 100)
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	for _, m := range msgs {
		if m.ID == id {
			return m.DAGTrace
		}
	}
	t.Fatalf("message %d not found", id)
	return ""
}

// The trace lands on the message it names, not the newest one.
func TestSetDAGTrace_WritesToTheMessageItNames(t *testing.T) {
	d := openTestDB(t)
	first, second := seedTwoAnswers(t, d)

	if err := d.SetDAGTrace(first, "s1", `[{"id":"n1"}]`); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := traceOf(t, d, "s1", first); got != `[{"id":"n1"}]` {
		t.Errorf("the named message has %q", got)
	}
	if got := traceOf(t, d, "s1", second); got != "" {
		t.Errorf("the newest message was written to instead: %q", got)
	}
}

// The case that broke in production: a later run saves its trace, and the
// earlier message keeps its own.
func TestSetDAGTrace_ALaterRunDoesNotClobberAnEarlierOne(t *testing.T) {
	d := openTestDB(t)
	first, second := seedTwoAnswers(t, d)

	if err := d.SetDAGTrace(first, "s1", `[{"id":"the real run"}]`); err != nil {
		t.Fatalf("first save: %v", err)
	}
	// The interjection: one node, saved afterwards, naming its own message.
	if err := d.SetDAGTrace(second, "s1", `[{"id":"inj1","type":"interjection"}]`); err != nil {
		t.Fatalf("second save: %v", err)
	}

	if got := traceOf(t, d, "s1", first); got != `[{"id":"the real run"}]` {
		t.Errorf("the first run's trace was replaced by the second's: %q", got)
	}
	if got := traceOf(t, d, "s1", second); got != `[{"id":"inj1","type":"interjection"}]` {
		t.Errorf("the second message has %q", got)
	}
}

// A message id from another conversation writes nothing. The session is a guard
// against a client naming a row it has no business in, not a lookup.
func TestSetDAGTrace_RefusesAMessageFromAnotherSession(t *testing.T) {
	d := openTestDB(t)
	first, _ := seedTwoAnswers(t, d)
	if err := d.CreateSession("s2", "web", "u1", "other"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	err := d.SetDAGTrace(first, "s2", `[{"id":"leaked"}]`)
	if !errors.Is(err, ErrNoSuchMessage) {
		t.Fatalf("err = %v, want ErrNoSuchMessage", err)
	}
	if got := traceOf(t, d, "s1", first); got != "" {
		t.Errorf("another session's trace was written onto it: %q", got)
	}
}

// A message that does not exist is reported, not silently ignored. A trace that
// went nowhere is otherwise found later as an empty panel with no record of why.
func TestSetDAGTrace_ReportsAMessageThatIsNotThere(t *testing.T) {
	d := openTestDB(t)
	seedTwoAnswers(t, d)
	if err := d.SetDAGTrace(999999, "s1", `[{"id":"n1"}]`); !errors.Is(err, ErrNoSuchMessage) {
		t.Fatalf("err = %v, want ErrNoSuchMessage", err)
	}
}

// The id AddMessage returns is the row it wrote, which is what everything above
// depends on.
func TestAddMessage_ReturnsTheRowItWrote(t *testing.T) {
	d := openTestDB(t)
	first, second := seedTwoAnswers(t, d)
	if first == 0 || second == 0 || first == second {
		t.Fatalf("ids are %d and %d", first, second)
	}
	if err := d.SetDAGTrace(second, "s1", `[{"id":"n2"}]`); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := traceOf(t, d, "s1", second); got != `[{"id":"n2"}]` {
		t.Errorf("the id did not name the row that was written: %q", got)
	}
}
