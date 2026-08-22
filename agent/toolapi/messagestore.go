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
