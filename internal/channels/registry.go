package channels

import "fmt"

// Registry holds all registered channels.
type Registry struct {
	channels map[string]Channel
}

// NewRegistry creates an empty channel registry.
func NewRegistry() *Registry {
	return &Registry{channels: make(map[string]Channel)}
}

// Register adds a channel to the registry.
func (r *Registry) Register(ch Channel) error {
	id := ch.ID()
	if _, exists := r.channels[id]; exists {
		return fmt.Errorf("channel %q already registered", id)
	}
	r.channels[id] = ch
	return nil
}

// Get returns a channel by ID.
func (r *Registry) Get(id string) (Channel, bool) {
	ch, ok := r.channels[id]
	return ch, ok
}
