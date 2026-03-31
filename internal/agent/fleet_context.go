package agent

import (
	"fmt"
	"strings"
	"time"
)

/*
 * FleetContextProvider supplies fleet situational awareness for agent prompts.
 * desc: Returns a markdown-formatted fleet context string, or empty string
 *       if no fleet data is available (e.g. standalone nodes).
 */
type FleetContextProvider interface {
	FleetContext() string
}

/*
 * fleetSection returns the fleet context block for system prompts.
 * desc: Returns empty string if no provider is configured (standalone nodes).
 *       Otherwise returns the fleet context prefixed with newlines.
 * return: fleet context markdown string, or empty string.
 */
func (a *Agent) fleetSection() string {
	if a.fleet == nil {
		return ""
	}
	ctx := a.fleet.FleetContext()
	if ctx == "" {
		return ""
	}
	return "\n\n" + ctx
}

/*
 * PeerSnapshot is the minimal peer data needed for fleet context.
 * desc: Avoids importing the peers package into the agent package.
 *       Contains node identity, role, active threats, and heartbeat timestamp.
 */
type PeerSnapshot struct {
	NodeID   string
	Hostname string
	IP       string
	Role     string
	Threats  []string
	LastSeen time.Time
}

/*
 * PeerSource provides peer data for fleet context building.
 * desc: Interface for retrieving snapshots of all known peers.
 */
type PeerSource interface {
	AllPeerSnapshots() []PeerSnapshot
}

/*
 * FleetContext builds the fleet context provider from a peer source.
 * desc: Holds a reference to the peer source and this node's identity
 *       for generating fleet-wide situational awareness prompts.
 */
type FleetContext struct {
	peers    PeerSource
	nodeID   string
	nodeRole string
}

/*
 * NewFleetContext creates a fleet context provider.
 * desc: Initializes a FleetContext with the given peer source and node identity.
 * param: peers - source of peer snapshot data.
 * param: nodeID - this node's unique identifier.
 * param: nodeRole - this node's role (e.g. "node", "coordinator").
 * return: pointer to the new FleetContext.
 */
func NewFleetContext(peers PeerSource, nodeID, nodeRole string) *FleetContext {
	return &FleetContext{peers: peers, nodeID: nodeID, nodeRole: nodeRole}
}

/*
 * FleetContext generates the fleet situational awareness markdown.
 * desc: Queries all peers, computes active/stale counts, aggregates threats
 *       across nodes (prioritizing repeated threats as campaign indicators),
 *       and formats the result as a markdown section.
 * return: markdown string with fleet status, or empty string if no peers.
 */
func (f *FleetContext) FleetContext() string {
	snapshots := f.peers.AllPeerSnapshots()
	if len(snapshots) == 0 {
		return ""
	}

	now := time.Now()
	staleThreshold := 5 * time.Minute

	var sb strings.Builder
	sb.WriteString("## Fleet Context\n")

	// Count active vs stale
	active := 0
	var stale []string
	for _, p := range snapshots {
		age := now.Sub(p.LastSeen)
		if age > staleThreshold {
			name := p.Hostname
			if name == "" {
				name = p.NodeID
			}
			stale = append(stale, fmt.Sprintf("%s (%s ago)", name, formatAge(age)))
		} else {
			active++
		}
	}

	total := len(snapshots)
	sb.WriteString(fmt.Sprintf("- **Reporting:** %d of %d nodes active\n", active, total))

	if len(stale) > 0 {
		// Show at most 5 stale nodes
		shown := stale
		if len(shown) > 5 {
			shown = shown[:5]
		}
		sb.WriteString(fmt.Sprintf("- **Stale (>%s):** %s", staleThreshold, strings.Join(shown, ", ")))
		if len(stale) > 5 {
			sb.WriteString(fmt.Sprintf(" (+%d more)", len(stale)-5))
		}
		sb.WriteString("\n")
	}

	// Collect all threats and find repeats (same threat on multiple nodes)
	threatNodes := make(map[string][]string) // threat description → node names
	for _, p := range snapshots {
		name := p.Hostname
		if name == "" {
			name = p.NodeID
		}
		for _, t := range p.Threats {
			threatNodes[t] = append(threatNodes[t], name)
		}
	}

	// Show top alerts, prioritise repeats
	type alertEntry struct {
		threat string
		nodes  []string
	}
	var repeats, singles []alertEntry
	for t, nodes := range threatNodes {
		if len(nodes) > 1 {
			repeats = append(repeats, alertEntry{t, nodes})
		} else {
			singles = append(singles, alertEntry{t, nodes})
		}
	}

	if len(repeats) > 0 || len(singles) > 0 {
		sb.WriteString("- **Top alerts across fleet:**\n")
		shown := 0
		maxAlerts := 8

		// Repeats first (campaign indicators)
		for _, a := range repeats {
			if shown >= maxAlerts {
				break
			}
			sb.WriteString(fmt.Sprintf("  - %s — seen on %d nodes (%s)\n",
				Text.Truncate(a.threat, 80), len(a.nodes), strings.Join(a.nodes, ", ")))
			shown++
		}
		// Then singles
		for _, a := range singles {
			if shown >= maxAlerts {
				break
			}
			sb.WriteString(fmt.Sprintf("  - %s: %s\n", a.nodes[0], Text.Truncate(a.threat, 80)))
			shown++
		}

		remaining := len(repeats) + len(singles) - shown
		if remaining > 0 {
			sb.WriteString(fmt.Sprintf("  - (+%d more alerts)\n", remaining))
		}
	}

	// This node identity
	sb.WriteString(fmt.Sprintf("- **This node:** %s (%s)\n", f.nodeID, f.nodeRole))

	return sb.String()
}

/*
 * formatAge formats a duration as a compact human-readable age string.
 * desc: Returns the most appropriate unit: seconds, minutes, hours, or days+hours.
 * param: d - the duration to format.
 * return: compact age string like "30s", "5m", "2h", or "1d 3h".
 */
func formatAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	if hours < 24 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dd %dh", hours/24, hours%24)
}
