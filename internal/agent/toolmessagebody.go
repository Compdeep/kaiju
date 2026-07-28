package agent

import (
	"strings"

	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
)

// toolMessageBody adapts a tool's ToolMessage envelope to the NodeBody
// interface, so tool output flows through the graph and edges like any other
// typed body.
//
// Field resolves ${node.X.field} into the tool's own payload (Data), preserving
// existing references exactly. The envelope's Status/Detail/Kind are the
// edge-facing framing signals, read via Envelope(), not Field — so a payload
// field named "status" never collides with the envelope's status.
type toolMessageBody struct {
	msg agenttools.ToolMessage
}

// Envelope exposes the framing signals (Status/Detail/Kind/Content) to edges
// and other typed consumers.
func (b toolMessageBody) Envelope() agenttools.ToolMessage { return b.msg }

// Field resolves a dot-path into the tool's payload (Data), matching the
// pre-envelope behavior. An optional leading "data." is tolerated so both
// ${node.X.field} and ${node.X.data.field} resolve.
func (b toolMessageBody) Field(path string) (any, bool) {
	p := strings.TrimPrefix(path, "data.")
	// Prefer the structured payload; fall back to Content for tools that carry
	// their whole result there (which may itself be JSON — e.g. reading a JSON
	// file), so field access is preserved exactly as pre-envelope.
	src := string(b.msg.Data)
	if src == "" {
		src = b.msg.Content
	}
	v, err := extractJSONFieldAny(src, p)
	if err != nil {
		return nil, false
	}
	return v, true
}

// Evidence renders what a downstream LLM node should see: content when present,
// an explicit absence/failure line otherwise, falling back to the raw payload
// for pure-data tools.
func (b toolMessageBody) Evidence() string {
	switch b.msg.Status {
	case agenttools.StatusEmpty:
		if b.msg.Detail != "" {
			return "(no " + b.msg.Kind + ": " + b.msg.Detail + ")"
		}
		return "(no " + b.msg.Kind + ")"
	case agenttools.StatusError:
		if b.msg.Detail != "" {
			return "(" + b.msg.Kind + " failed: " + b.msg.Detail + ")"
		}
		return "(" + b.msg.Kind + " failed)"
	}
	if b.msg.Content != "" {
		return b.msg.Content
	}
	if len(b.msg.Data) > 0 {
		return string(b.msg.Data)
	}
	return ""
}

// Summary renders a short trace line for the frontend.
func (b toolMessageBody) Summary() string {
	s := b.msg.Kind + " " + string(b.msg.Status)
	if b.msg.Detail != "" {
		s += ": " + Text.TruncateLog(b.msg.Detail, 120)
	}
	return s
}
