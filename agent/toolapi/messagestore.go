package toolapi

import "context"

// MessageStore is where an application keeps its conversation messages, and what
// message_search reads.
//
// It is an interface rather than this project's own database because an
// application built on the engine keeps its conversations wherever it keeps
// everything else. One that satisfies these two methods gets the tool; one that
// does not gets an agent with no way to look further back than its own context
// window, which is the thing the tool exists to fix.
type MessageStore interface {
	// SearchMessages returns messages whose text matches query, best match
	// first. An empty sessionID searches every session; limit is the most rows
	// to return, and an implementation may return fewer.
	SearchMessages(ctx context.Context, query, sessionID string, limit int) ([]FoundMessage, error)

	// RecallMessages returns earlier messages of one conversation that mention
	// any of the terms, together with the messages around them, in the order
	// they were written.
	//
	// Any rather than all: the terms come from a model reading the question, and
	// requiring every one of them would return nothing whenever it offered a
	// word the conversation never used. Around them, because a reply that
	// matches is often meaningless without the question it answered.
	RecallMessages(ctx context.Context, sessionID string, terms []string, opts Recall) ([]FoundMessage, error)
}

// Recall bounds what one recall returns, so a long conversation cannot put more
// into a prompt than the prompt has room for.
type Recall struct {
	// SkipNewest is how many of the most recent messages to leave out, being the
	// ones already in front of the model. Zero searches the whole conversation.
	SkipNewest int

	// Hits is how many matching messages to build the answer around.
	Hits int

	// Around is how many messages either side of each match to include.
	Around int

	// MaxChars is the most text to return. Messages are dropped whole, oldest
	// first, so what comes back is never a message cut in half.
	MaxChars int
}

// FoundMessage is one message a search matched.
//
// It carries the session it came from as well as the text, because the answer to
// "when did we discuss this" is a conversation, not a sentence, and an agent
// given only the sentence cannot go and read the rest.
type FoundMessage struct {
	ID           int64  `json:"id" desc:"the message's own identifier"`
	SessionID    string `json:"session_id" desc:"the conversation it belongs to"`
	SessionTitle string `json:"session_title" desc:"that conversation's title, if it has one"`
	Role         string `json:"role" desc:"who wrote it — user, assistant or system"`
	Content      string `json:"content" desc:"the message text"`
	CreatedAt    int64  `json:"created_at" desc:"when it was written, seconds since the epoch"`
}
