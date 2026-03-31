package channels

import "context"

// Channel is the interface every messaging adapter must implement.
type Channel interface {
	// ID returns the channel type identifier (e.g. "cli", "web", "telegram").
	ID() string

	// Start begins listening for inbound messages, sending them on inbox.
	// Blocks until ctx is cancelled.
	Start(ctx context.Context, inbox chan<- InboundMessage) error

	// Send delivers an outbound message through this channel.
	Send(ctx context.Context, msg OutboundMessage) error

	// Close releases any resources held by the channel.
	Close() error
}
