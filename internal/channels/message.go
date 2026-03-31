package channels

import "time"

// InboundMessage represents a message received from a user via any channel.
type InboundMessage struct {
	ChannelID  string            // which channel type (e.g. "cli", "telegram")
	SessionID  string            // session/conversation identifier
	SenderID   string            // user identifier
	SenderName string            // display name
	Text       string            // message body
	ReplyTo    string            // message ID being replied to (optional)
	Metadata   map[string]string // channel-specific metadata
	Timestamp  time.Time
}

// OutboundMessage represents a message to send back to a user.
type OutboundMessage struct {
	ChannelID   string            // target channel type
	SessionID   string            // session/conversation identifier
	RecipientID string            // target user/chat
	Text        string            // message body
	ReplyTo     string            // reply to message ID (optional)
	Metadata    map[string]string // channel-specific metadata
}
