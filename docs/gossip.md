# Gossip Module — P2P Agent Mesh

> **Status:** Optional module. Not required for standalone operation.
> **Package:** `pkg/gossip`

## Overview

The gossip module provides peer-to-peer networking between kaiju instances using libp2p and GossipSub. Multiple agents can discover each other, share context, coordinate tasks, and form a distributed mesh.

Originally built for omamori's fleet security architecture, the module is extracted here as a standalone, pluggable component.

## Architecture

```
┌─────────────┐     GossipSub      ┌─────────────┐
│   Kaiju A    │◄──────────────────►│   Kaiju B    │
│  (desktop)   │     libp2p mesh    │  (server)    │
└──────┬───────┘                    └──────┬───────┘
       │                                    │
       ▼                                    ▼
   Agent Engine                        Agent Engine
```

## Key Concepts

### Topics

The mesh uses four GossipSub topics:

| Topic | Who publishes | Purpose |
|-------|---------------|---------|
| `/kaiju/pulse` | All peers | Heartbeat — status, capabilities, load |
| `/kaiju/beacon` | Coordinators only | Commands, task delegation, roster sync |
| `/kaiju/alerts` | All peers | Shared findings, results, notifications |
| `/kaiju/murmur` | Selective | Peer-to-peer enrollment, handshakes |

### Roles

| Role | Clearance | Description |
|------|-----------|-------------|
| Pawn | 1 | Standard agent — executes tasks, publishes results |
| Knight | 1 | Trusted agent — same as pawn with higher vote weight |
| Queen | 2 | Coordinator — delegates tasks, manages roster, publishes beacons |

### Peer Discovery

- **mDNS**: Automatic discovery on local network (zero config)
- **Bootstrap peers**: Explicit multiaddr list for cross-network connectivity
- **Roster**: Coordinator-managed enrollment with invite tokens

## Protocol

### Message Envelope

Every message carries a protocol envelope:

```json
{
  "v": 1,
  "id": "a3f8c1-0042",
  "node": "a3f8c1",
  "t": 1739836800
}
```

- `v`: Protocol version (always 1)
- `id`: Monotonically increasing per-node (`{nodeID}-{seq:04d}`)
- `node`: Short node identifier (first 6 chars of peer ID)
- `t`: Unix timestamp

### Sequencer

Each node has a `Sequencer` that generates unique, monotonic message IDs and stamps outgoing messages with the envelope.

## Mesh Parameters

```
MeshDegree:     3    (target peers per topic)
MeshDegreeLow:  2    (graft new peers below this)
MeshDegreeHigh: 6    (prune excess peers above this)
FloodPublish:   true (flood own messages to all connected peers)
```

## Roster Management

- **Enrollment**: Token-based invite system. Coordinators generate tokens, peers redeem them.
- **Promotion**: Pawns can request promotion to knight. Requires coordinator approval.
- **Revocation**: Coordinators can revoke peers, adding them to a persistent blocklist.
- **Roster sync**: Coordinators broadcast roster state via beacon topic for new peers.

## Security Layers

1. **Transport**: libp2p Noise protocol — encrypted, authenticated connections
2. **Network**: Topic validators gate publishing by enrollment status and role
3. **Application**: Blocklist enforcement, role-based authorization, confidence thresholds
4. **Optional**: Pre-shared key (swarm.key) for private network mode

## Integration with Agent

The gossip module integrates through the `GossipPublisher` interface:

```go
type GossipPublisher interface {
    PublishAlert(ctx context.Context, data []byte) error
    PublishMurmur(ctx context.Context, data []byte) error
    Sequencer() *protocol.Sequencer
}
```

When gossip is disabled, a no-op publisher is used (zero overhead).

When enabled, the agent can:
- Publish findings to the mesh after investigations
- Receive triggers from peer alerts
- Coordinate with other agents via beacon commands

## Dependencies

```
github.com/libp2p/go-libp2p         — Host, peer identity, transport
github.com/libp2p/go-libp2p-pubsub  — GossipSub topic mesh
```

## Usage

Gossip is opt-in. To enable:

```json
{
  "gossip": {
    "enabled": true,
    "port": 0,
    "bootstrap_peers": [],
    "mesh_degree": 3,
    "pulse_interval_sec": 300
  }
}
```

When disabled (default), kaiju runs as a standalone assistant with no network overhead.

## Use Cases

- **Multi-device sync**: Desktop and server kaiju instances share context
- **Task delegation**: Coordinator distributes work across specialized agents
- **Shared memory**: Agents publish findings that enrich each other's investigations
- **Redundancy**: If one agent is busy, peers can pick up incoming tasks
