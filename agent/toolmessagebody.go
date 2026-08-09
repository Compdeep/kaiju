package agent

import (
	"strings"

	agenttools "github.com/Compdeep/kaiju/agent/tools"
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
	// Strip the envelope's payload wrapper in either form: "data.field" → "field",
	// and bare "data" → "" (the whole payload). This body already resolves against
	// the payload, so a leading "data" is always the wrapper, never a real field —
	// tolerating only the dotted form left ${node.X.data} failing at runtime even
	// though plan-time validation accepts it.
	// An unclassified body has no payload — its whole result is the text the
	// producer returned. Resolve from the top of that text, which is what
	// RawTextBody did for the same output before it carried an envelope, so a
	// reference like ${node.X.count} into a tool's own JSON keeps working.
	if b.msg.Status == agenttools.StatusUnclassified {
		if path == "" {
			return b.msg.Content, true
		}
		v, err := extractJSONFieldAny(b.msg.Content, path)
		if err != nil {
			return nil, false
		}
		return v, true
	}

	p := strings.TrimPrefix(path, "data.")
	if path == "data" {
		p = ""
	}
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
	case agenttools.StatusUnclassified:
		// The producer's text, unchanged. Adding a note here would put a frame
		// around every prose tool's output for the model to read past.
		return b.msg.Content
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
	// An unclassified body has nothing to summarise but its text, and "text
	// unclassified" tells a reader less than the first line does. This is what
	// the trace showed for the same output before it carried an envelope.
	if b.msg.Status == agenttools.StatusUnclassified {
		for _, line := range strings.Split(b.msg.Content, "\n") {
			if t := strings.TrimSpace(line); t != "" {
				return Text.TruncateLog(t, 120)
			}
		}
		return ""
	}
	s := b.msg.Kind + " " + string(b.msg.Status)
	if b.msg.Detail != "" {
		s += ": " + Text.TruncateLog(b.msg.Detail, 120)
	}
	return s
}

/*
 * NewToolBody wraps a tool's envelope as a node body.
 * desc: Exported because an application supplying Capabilities.Answer needs to
 *       build a graph to test that answer against, and a graph whose tool nodes
 *       carry plain strings is not the graph it will be given — the coverage
 *       edge reads the envelope's status to find gaps, and finds none on a
 *       string. Without this the difference is invisible: the test passes, the
 *       prompt is quietly shorter, and the answer is written on evidence with a
 *       hole in it.
 * param: msg - what the tool returned.
 * return: the body to hand to Graph.SetBody.
 */
func NewToolBody(msg agenttools.ToolMessage) NodeBody { return toolMessageBody{msg: msg} }
