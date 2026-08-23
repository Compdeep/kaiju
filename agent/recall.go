package agent

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// How much of the conversation one recall may put back in front of the model.
const (
	recallHits     = 3
	recallAround   = 1
	recallMaxChars = 4000
)

/*
 * SetMessageStore gives the agent somewhere to read earlier messages from.
 * desc: Enables recall in the chat lane. Without it the router may still ask
 *       for earlier context and nothing will be looked up.
 * param: store - the conversation store, or nil to turn recall off
 */
// SetMessageStore is set after construction rather than taken as configuration
// because the database is opened after the agent is built, and an agent that
// waited for it would not exist in time to be given one.
func (a *Agent) SetMessageStore(store toolapi.MessageStore) {
	a.messages = store
}

// recall looks up what the router said the answer needs.
//
// Empty on almost every turn: the router leaves the words out unless the latest
// message refers to something it cannot see, and a conversation short enough to
// fit in the window has nothing older to find.
func (a *Agent) recall(ctx context.Context, t ChatTurn, terms []string) []toolapi.FoundMessage {
	if len(terms) == 0 || a.messages == nil || t.SessionID == "" {
		return nil
	}
	found, err := a.messages.RecallMessages(ctx, t.SessionID, terms, toolapi.Recall{
		// Everything the model is already being sent, so what comes back is only
		// what it cannot see. Counted the way the store counts, which leaves the
		// summary out: it is a system message, and it is in the prompt already.
		SkipNewest: countSpoken(t.History),
		Hits:       recallHits,
		Around:     recallAround,
		MaxChars:   recallMaxChars,
	})
	if err != nil {
		// Reported and dropped. A conversation that cannot be searched is still a
		// conversation the model can answer from, so this ends the recall and not
		// the turn.
		log.Printf("[recall] %s: %v", t.SessionID, err)
		return nil
	}
	log.Printf("[recall] %s: %d earlier message(s) for %v", t.SessionID, len(found), terms)
	return found
}

// countSpoken is how many of the messages being sent were written by someone,
// rather than assembled for the model.
func countSpoken(history []llm.Message) int {
	n := 0
	for _, m := range history {
		if m.Role != "system" {
			n++
		}
	}
	return n
}

// recallBlock is the recalled messages as one message to put in the request.
//
// Introduced as earlier messages that matched the words, and never as fact: the
// words were guessed from the question, so a message can match without being
// about the same thing, and a model told plainly where they came from can weigh
// them instead of answering from them.
func recallBlock(found []toolapi.FoundMessage, terms []string) string {
	if len(found) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Earlier messages from this same conversation, from before the part you can see. ")
	b.WriteString("They were found by looking for: " + strings.Join(terms, ", ") + ". ")
	b.WriteString("They may not be relevant to the current message.\n\n")
	for _, f := range found {
		if f.CreatedAt > 0 {
			b.WriteString(time.Unix(f.CreatedAt, 0).UTC().Format("2006-01-02 15:04") + " ")
		}
		b.WriteString(f.Role + ": " + f.Content + "\n\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// withRecall puts the recalled block into the request, immediately before the
// message it was recalled for.
//
// Before the last message rather than at the top: it was fetched to answer this
// message, and placed above the history it would read as something said at the
// start of the conversation and long since moved past.
func withRecall(messages []llm.Message, block string) []llm.Message {
	if block == "" || len(messages) == 0 {
		return messages
	}
	at := len(messages) - 1
	out := make([]llm.Message, 0, len(messages)+1)
	out = append(out, messages[:at]...)
	out = append(out, llm.Message{Role: "system", Content: block})
	return append(out, messages[at:]...)
}
