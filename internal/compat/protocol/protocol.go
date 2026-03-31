// Package protocol provides a compatibility shim for the omamori gossip protocol.
// Only the Sequencer type is used by the agent (via GossipPublisher interface).
package protocol

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"
)

// Envelope is a gossip message envelope.
type Envelope struct {
	V    int    `json:"v"`
	ID   string `json:"id"`
	Node string `json:"node"`
	T    int64  `json:"t"`
}

// Sequencer generates unique, monotonic envelope IDs.
type Sequencer struct {
	nodeID string
	seq    atomic.Uint64
}

// NewSequencer creates a sequencer for the given node ID.
func NewSequencer(nodeID string) *Sequencer {
	return &Sequencer{nodeID: nodeID}
}

// NodeID returns the node identifier.
func (s *Sequencer) NodeID() string { return s.nodeID }

// Next returns a new envelope with a unique ID.
func (s *Sequencer) Next() Envelope {
	n := s.seq.Add(1)
	return Envelope{
		V:    1,
		ID:   fmt.Sprintf("%s-%04d", s.nodeID, n),
		Node: s.nodeID,
		T:    time.Now().Unix(),
	}
}

// Stamp injects envelope fields into the given value and returns JSON bytes.
func (s *Sequencer) Stamp(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}
