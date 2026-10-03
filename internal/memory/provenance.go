package memory

import (
	"encoding/json"
	"strings"
)

// What a stored turn actually did, read back off its DAG trace.
//
// A turn's role and content do not say whether it ran anything. An assistant
// message that fired fourteen tools and wrote three files is the same shape as one
// the tool-less chat lane invented, so a later turn reading the thread cannot tell
// a result from a claim. One of them answered "Confirmed in source and generated
// outputs" about files a previous turn had written, which from where it stood was
// indistinguishable from honest work.
//
// Nothing new is recorded for this. Every assistant row already carries dag_trace,
// and GetRecentMessages already selects it — it was being dropped where the rows
// became llm.Messages.

// traceNode is the part of agent.NodeInfo this needs. Declared here rather than
// imported so internal/memory does not depend on the agent package; the two fields
// are in the frozen trace format and a test pins them against real stored traces.
type traceNode struct {
	Type string `json:"type"`
	Tool string `json:"tool"`
}

// provenanceOf returns the tools a turn ran, comma separated and in the order they
// first appear, or "" when the turn ran none or nothing is known.
//
// "" covers three different cases on purpose — no trace was stored, the trace will
// not parse, and the turn ran no tools. A caller must treat all three as "say
// nothing": most stored history predates tracing, and chat-lane, vision and CLI
// replies never write a trace at all, so reporting absence as "ran nothing" would
// describe the back catalogue as fabricated.
func provenanceOf(dagTrace string) string {
	if strings.TrimSpace(dagTrace) == "" {
		return ""
	}
	var nodes []traceNode
	if err := json.Unmarshal([]byte(dagTrace), &nodes); err != nil {
		return ""
	}
	seen := map[string]bool{}
	var tools []string
	for _, n := range nodes {
		// compute is the coder: it writes files, so it counts as work done even
		// though the thing it ran is not a named tool.
		if n.Type != "tool" && n.Type != "compute" {
			continue
		}
		name := n.Tool
		if name == "" {
			name = n.Type
		}
		if !seen[name] {
			seen[name] = true
			tools = append(tools, name)
		}
	}
	return strings.Join(tools, ", ")
}
