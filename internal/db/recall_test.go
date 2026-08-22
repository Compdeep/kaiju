package db

import (
	"context"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// a conversation long enough to have a past
func recallDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(t.TempDir() + "/recall.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.CreateSession("s1", "web", "u", ""); err != nil {
		t.Fatal(err)
	}
	say := func(role, text string) {
		if err := d.AddMessage("s1", role, text); err != nil {
			t.Fatal(err)
		}
	}
	say("user", "how long should we keep the audit rows")
	say("assistant", "fourteen days is the usual choice")
	say("user", "fine, fourteen it is")
	say("assistant", "recorded")
	for i := 0; i < 20; i++ {
		say("user", "something else entirely")
		say("assistant", "quite")
	}
	return d
}

func contents(found []toolapi.FoundMessage) string {
	var b strings.Builder
	for _, f := range found {
		b.WriteString(f.Role + ": " + f.Content + "\n")
	}
	return b.String()
}

// Any term is enough, so a word the conversation never used does not empty the
// result.
func TestRecallMatchesAnyTerm(t *testing.T) {
	d := recallDB(t)
	found, err := d.RecallMessages(context.Background(), "s1",
		[]string{"retention", "audit", "kestrels"}, toolapi.Recall{SkipNewest: 30})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("found nothing though audit was mentioned")
	}
	if !strings.Contains(contents(found), "audit rows") {
		t.Errorf("the matching message is not there:\n%s", contents(found))
	}
}

// The messages around a match come with it, so a reply arrives with the
// question it answered.
func TestRecallBringsTheMessagesAround(t *testing.T) {
	d := recallDB(t)
	found, err := d.RecallMessages(context.Background(), "s1",
		[]string{"fourteen"}, toolapi.Recall{SkipNewest: 30, Around: 1})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	got := contents(found)
	if !strings.Contains(got, "how long should we keep the audit rows") {
		t.Errorf("the question before the match is missing:\n%s", got)
	}
	if !strings.Contains(got, "recorded") {
		t.Errorf("the message after the match is missing:\n%s", got)
	}
}

// What comes back is in the order it was written, and no message appears twice
// however much the windows overlap.
func TestRecallIsOrderedAndWithoutRepeats(t *testing.T) {
	d := recallDB(t)
	found, _ := d.RecallMessages(context.Background(), "s1",
		[]string{"fourteen", "audit", "recorded"}, toolapi.Recall{SkipNewest: 30, Around: 2})
	seen := map[int64]bool{}
	var last int64
	for _, f := range found {
		if seen[f.ID] {
			t.Errorf("message %d came back twice", f.ID)
		}
		seen[f.ID] = true
		if f.ID < last {
			t.Errorf("out of order: %d after %d", f.ID, last)
		}
		last = f.ID
	}
}

// The newest messages are left out: they are the ones already in front of the
// model, and returning them would spend the budget on what it can see.
func TestRecallSkipsWhatTheModelCanSee(t *testing.T) {
	d := recallDB(t)
	found, _ := d.RecallMessages(context.Background(), "s1",
		[]string{"something"}, toolapi.Recall{SkipNewest: 40})
	if len(found) != 0 {
		t.Errorf("returned %d messages that are inside the recent window:\n%s", len(found), contents(found))
	}
	// With nothing skipped the same term is found.
	found, _ = d.RecallMessages(context.Background(), "s1",
		[]string{"something"}, toolapi.Recall{})
	if len(found) == 0 {
		t.Error("found nothing with nothing skipped")
	}
}

// A summary is never recalled: it is already in the prompt this is filling.
func TestRecallLeavesTheSummaryOut(t *testing.T) {
	d := recallDB(t)
	if _, err := d.PrependMessage("s1", "system", "[Conversation summary]: we discussed audit retention", 1); err != nil {
		t.Fatal(err)
	}
	found, _ := d.RecallMessages(context.Background(), "s1",
		[]string{"audit"}, toolapi.Recall{SkipNewest: 30, Around: 1})
	for _, f := range found {
		if f.Role == "system" || strings.Contains(f.Content, "Conversation summary") {
			t.Errorf("the summary was recalled: %q", f.Content)
		}
	}
	if len(found) == 0 {
		t.Error("leaving the summary out left nothing at all")
	}
}

// The budget is kept by dropping whole messages, never by cutting one.
func TestRecallKeepsWithinItsBudget(t *testing.T) {
	d := recallDB(t)
	found, _ := d.RecallMessages(context.Background(), "s1",
		[]string{"audit", "fourteen"}, toolapi.Recall{SkipNewest: 30, Around: 3, MaxChars: 60})
	total := 0
	for _, f := range found {
		total += len(f.Content)
	}
	if len(found) > 1 && total > 60 {
		t.Errorf("returned %d characters against a budget of 60", total)
	}
	for _, f := range found {
		if strings.HasSuffix(f.Content, "…") {
			t.Error("a message was cut rather than dropped")
		}
	}
}

// Nothing to look for is not an error, and neither is a conversation with no
// match in it.
func TestRecallWithNothingToFind(t *testing.T) {
	d := recallDB(t)
	for _, terms := range [][]string{nil, {}, {"", "   "}, {`"`}} {
		found, err := d.RecallMessages(context.Background(), "s1", terms, toolapi.Recall{})
		if err != nil || len(found) != 0 {
			t.Errorf("terms %q: %d found, err=%v", terms, len(found), err)
		}
	}
	found, err := d.RecallMessages(context.Background(), "s1", []string{"kestrels"}, toolapi.Recall{})
	if err != nil || len(found) != 0 {
		t.Errorf("a term nobody used: %d found, err=%v", len(found), err)
	}
}

// Japanese words are found, since the model writing the terms is what decides
// where the words begin and end.
func TestRecallFindsJapanese(t *testing.T) {
	d, _ := Open(t.TempDir() + "/jp.db")
	defer d.Close()
	d.CreateSession("s1", "web", "u", "")
	d.AddMessage("s1", "user", "タイムアウトの値をどうしますか")
	d.AddMessage("s1", "assistant", "三十秒にしましょう")
	for i := 0; i < 10; i++ {
		d.AddMessage("s1", "user", "別の話です")
	}
	found, err := d.RecallMessages(context.Background(), "s1",
		[]string{"タイムアウト"}, toolapi.Recall{SkipNewest: 5, Around: 1})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	got := contents(found)
	if !strings.Contains(got, "タイムアウト") {
		t.Errorf("the Japanese message was not recalled:\n%s", got)
	}
	if !strings.Contains(got, "三十秒") {
		t.Errorf("the reply beside it was not recalled:\n%s", got)
	}
}
