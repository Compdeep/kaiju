package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// toolMessageBody adapts a tool's ToolMessage envelope to the NodeBody
// interface, so tool output flows through the graph and edges like any other
// typed body.
//
// The point of the envelope is that presence, absence and failure become a
// typed signal rather than prose a later stage has to infer from. A tool that
// found nothing says so in Status, instead of returning an empty string that
// reads like a result.
//
// Field resolves ${node.X.field} into the tool's own payload (Data), preserving
// existing references exactly. The envelope's Status/Detail/Kind are the
// edge-facing framing signals, read via Envelope(), not Field — so a payload
// field named "status" never collides with the envelope's status.
type toolMessageBody struct {
	msg toolapi.ToolMessage
}

// Envelope exposes the framing signals (Status/Detail/Kind/Content) to edges
// and other typed consumers.
func (b toolMessageBody) Envelope() toolapi.ToolMessage { return b.msg }

// Field resolves a dot-path into the tool's payload (Data), matching the
// pre-envelope behavior. An optional leading "data." is tolerated so both
// ${node.X.field} and ${node.X.data.field} resolve. The envelope's own names —
// content, detail, status, type — resolve to the envelope, since those are
// what EnvelopeSchema declares to the planner.
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
	if b.msg.Status == toolapi.StatusUnclassified {
		if path == "" {
			return b.msg.Content, true
		}
		v, err := extractJSONFieldAny(b.msg.Content, path)
		if err != nil {
			return b.envelopeField(path)
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
		return b.envelopeField(path)
	}
	return v, true
}

// envelopeField resolves the envelope's own top-level names, which
// EnvelopeSchema declares and a planner therefore reads as valid paths. Without
// it, ${node.X.content} passes plan-time validation against the declared schema
// and then fails at fire time — and a tool whose readable half is Content with
// only counts in Data has no working path to its text at all.
//
// The payload wins where both have the name: web_fetch's payload carries the
// HTTP status, and ${node.X.status} has always meant that one. Only the bare
// form falls back, so ${node.X.data.status} stays the payload's.
func (b toolMessageBody) envelopeField(path string) (any, bool) {
	switch path {
	case "content":
		return b.msg.Content, true
	case "detail":
		return b.msg.Detail, true
	case "status":
		return string(b.msg.Status), true
	case "type":
		return b.msg.Type, true
	}
	return nil, false
}

// Evidence renders what a downstream LLM node should see: content when present,
// an explicit absence/failure line otherwise, falling back to the raw payload
// for pure-data tools.
func (b toolMessageBody) Evidence() string {
	switch b.msg.Status {
	case toolapi.StatusUnclassified:
		// The producer's text, unchanged. Adding a note here would put a frame
		// around every prose tool's output for the model to read past.
		return b.msg.Content
	case toolapi.StatusEmpty:
		if b.msg.Detail != "" {
			return "(no " + b.msg.Type + ": " + b.msg.Detail + ")"
		}
		return "(no " + b.msg.Type + ")"
	case toolapi.StatusError:
		line := "(" + b.msg.Type + " failed)"
		if b.msg.Detail != "" {
			line = "(" + b.msg.Type + " failed: " + b.msg.Detail + ")"
		}
		// What the tool did produce, after the line saying it failed. A command
		// that ran and exited non-zero usually printed the only thing that says
		// why — "Permission denied" — and a tool that keeps it in the payload so
		// it survives for templates and the frontend was still not showing it to
		// the model, which read the exit status alone.
		if out := b.failureOutput(); out != "" {
			return line + "\n" + out
		}
		return line
	}
	if b.msg.Content != "" {
		return b.msg.Content + b.handleLine()
	}
	if len(b.msg.Data) > 0 {
		return string(b.msg.Data)
	}
	return ""
}

// handleFields are payload fields that name something the result did not carry
// inline: a file holding the whole of what a tool produced, when the content
// above is a bounded part of it.
//
// Why they need naming at all: a stage's whole view of an earlier step is the
// text Evidence returns, and that text has always been Content alone. Anything
// in the payload was reachable by ${node.N.field} and discoverable by nobody —
// a planner writing the next step could not see that a field existed, so a tool
// that kept the rest of its output on disk was keeping it for a reader who
// would never learn of it.
//
// Read from a run that had been told a path exists and was given none: it
// invented one from the URL, and the step that followed failed on a file that
// was never there.
var handleFields = []struct{ key, says string }{
	{"full_content_path", "the whole page is in this file"},
	{"output_path", "the command's whole output is in this file"},
}

/*
 * handleLine names a file the payload points at, for the text a stage reads.
 * desc: One line after the content, only when the payload holds such a field.
 *       It says the name to reference — the field, not the value — because a
 *       later step reaches the file by wiring ${step.N.<field>} and not by
 *       copying a path out of prose it read.
 * return: the line, starting with a newline, or "" when there is no such field.
 */
func (b toolMessageBody) handleLine() string {
	if len(b.msg.Data) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(b.msg.Data, &payload); err != nil {
		return ""
	}
	for _, f := range handleFields {
		v, ok := payload[f.key].(string)
		if !ok || v == "" {
			continue
		}
		// Both the value and the way to reference it. The value, because a
		// stage that cannot see the real path invents a plausible one — that is
		// what happened, and the step after it failed on a file that was never
		// there. The reference form, because wiring it is what survives a
		// re-plan, while a path copied out of prose is a literal that goes stale.
		line := fmt.Sprintf("\n\n[%s: %s — %s. Reference it as ${step.N.%s} in a later step.]",
			f.key, f.says, v, f.key)
		if cut, _ := payload[truncatedFlagFor(f.key)].(bool); cut {
			line += "\n[it holds the beginning of what was produced, not all of it]"
		}
		return line
	}
	return ""
}

// truncatedFlagFor names the payload field that says a kept file is partial.
func truncatedFlagFor(key string) string {
	if key == "output_path" {
		return "output_truncated"
	}
	return "body_truncated"
}

// failureOutput is whatever a failed tool produced: its rendered text, or its
// payload when it carries no text. Empty when it produced neither, which is the
// case for a tool that could not start.
func (b toolMessageBody) failureOutput() string {
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
	if b.msg.Status == toolapi.StatusUnclassified {
		for _, line := range strings.Split(b.msg.Content, "\n") {
			if t := strings.TrimSpace(line); t != "" {
				return Text.TruncateLog(t, 120)
			}
		}
		return ""
	}
	s := b.msg.Type + " " + string(b.msg.Status)
	if b.msg.Detail != "" {
		s += ": " + Text.TruncateLog(b.msg.Detail, 120)
	}
	return s
}

/*
 * NewToolBody wraps a tool's envelope as a node body.
 * desc: Exported because an application supplying Handlers.Answer needs to
 *       build a graph to test that answer against, and a graph whose tool nodes
 *       carry plain strings is not the graph it will be given — the coverage
 *       edge reads the envelope's status to find gaps, and finds none on a
 *       string. Without this the difference is invisible: the test passes, the
 *       prompt is quietly shorter, and the answer is written on evidence with a
 *       hole in it.
 * param: msg - what the tool returned.
 * return: the body to hand to Graph.SetBody.
 */
func NewToolBody(msg toolapi.ToolMessage) NodeBody { return toolMessageBody{msg: msg} }
