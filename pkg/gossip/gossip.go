// Package gossip provides an optional peer-to-peer mesh networking module
// for kaiju agents using libp2p and GossipSub.
//
// When enabled, multiple kaiju instances can discover each other on the local
// network (mDNS) or via bootstrap peers, forming a distributed mesh that
// shares context, coordinates tasks, and publishes findings.
//
// The module integrates with the agent engine through the GossipPublisher
// interface defined in internal/agent. When disabled, a no-op publisher is
// used with zero overhead.
//
// This package defines the interfaces and types. The full libp2p implementation
// is in gossip/hub.go (requires libp2p dependencies).
package gossip

import (
	"context"

	"github.com/Compdeep/kaiju/internal/compat/protocol"
)

// Publisher is the interface the agent uses to publish messages to the mesh.
// This matches the agent.GossipPublisher interface.
type Publisher interface {
	PublishAlert(ctx context.Context, data []byte) error
	PublishMurmur(ctx context.Context, data []byte) error
	Sequencer() *protocol.Sequencer
}

// Subscriber receives messages from the mesh.
type Subscriber interface {
	OnPulse(handler func(from string, data []byte))
	OnAlert(handler func(from string, data []byte))
	OnBeacon(handler func(from string, data []byte))
	OnMurmur(handler func(from string, data []byte))
}

// Config holds gossip mesh configuration.
type Config struct {
	Enabled        bool     `json:"enabled"`
	Port           int      `json:"port"`             // 0 = auto-bind
	BootstrapPeers []string `json:"bootstrap_peers"`  // multiaddr list
	MeshDegree     int      `json:"mesh_degree"`      // target peers per topic (default: 3)
	MeshDegreeLow  int      `json:"mesh_degree_low"`  // graft threshold (default: 2)
	MeshDegreeHigh int      `json:"mesh_degree_high"` // prune threshold (default: 6)
	FloodPublish   bool     `json:"flood_publish"`    // flood own messages (default: true)
	PulseIntervalSec int   `json:"pulse_interval_sec"` // heartbeat interval (default: 300)
	SwarmKeyPath   string   `json:"swarm_key_path"`   // PSK for private network (optional)
	RosterPath     string   `json:"roster_path"`      // peer enrollment registry
}

// DefaultConfig returns gossip configuration with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:          false,
		Port:             0,
		MeshDegree:       3,
		MeshDegreeLow:    2,
		MeshDegreeHigh:   6,
		FloodPublish:     true,
		PulseIntervalSec: 300,
	}
}

// Topics used by the gossip mesh.
const (
	TopicPulse  = "/kaiju/pulse"
	TopicBeacon = "/kaiju/beacon"
	TopicAlerts = "/kaiju/alerts"
	TopicMurmur = "/kaiju/murmur"
)

// Role defines a peer's position in the mesh hierarchy.
type Role string

const (
	RolePawn   Role = "pawn"   // Standard agent
	RoleKnight Role = "knight" // Trusted agent
	RoleQueen  Role = "queen"  // Coordinator
)

// PeerInfo describes a known peer in the mesh.
type PeerInfo struct {
	NodeID   string `json:"node_id"`
	PeerID   string `json:"peer_id"`
	Role     Role   `json:"role"`
	Hostname string `json:"hostname,omitempty"`
	LastSeen int64  `json:"last_seen"`
}

// NoopPublisher satisfies Publisher with zero overhead.
type NoopPublisher struct {
	seq *protocol.Sequencer
}

// NewNoopPublisher creates a no-op publisher for standalone mode.
func NewNoopPublisher(nodeID string) *NoopPublisher {
	return &NoopPublisher{seq: protocol.NewSequencer(nodeID)}
}

func (n *NoopPublisher) PublishAlert(_ context.Context, _ []byte) error  { return nil }
func (n *NoopPublisher) PublishMurmur(_ context.Context, _ []byte) error { return nil }
func (n *NoopPublisher) Sequencer() *protocol.Sequencer                  { return n.seq }
