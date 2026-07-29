package tools

import "encoding/json"

// ToolStatus is the uniform outcome signal every tool reports, so an edge can
// frame presence / absence / failure the same way regardless of which tool ran.
type ToolStatus string

const (
	StatusOK    ToolStatus = "ok"    // produced usable content
	StatusEmpty ToolStatus = "empty" // ran fine, nothing to show — absence is a real result
	StatusError ToolStatus = "error" // failed
)

// ToolMessage is the uniform output envelope every tool emits. The tool-specific
// payload lives verbatim in Data; the envelope adds only the signals an edge
// needs to frame context: what kind of output, did it succeed, is it empty or
// failed and why. It is the "structured communication" layer between tools and
// the graph — deliberately NOT a change to what each tool computes.
type ToolMessage struct {
	Kind    string          `json:"kind"`              // payload discriminator: search|page|file|listing|command|kv|status|text
	Status  ToolStatus      `json:"status"`            // ok | empty | error — the framing signal
	Content string          `json:"content,omitempty"` // renderable evidence text
	Detail  string          `json:"detail,omitempty"`  // why: the note when empty, the reason when error
	Data    json.RawMessage `json:"data,omitempty"`    // the tool's own payload, verbatim + field-addressable
}

func marshalData(data any) json.RawMessage {
	if data == nil {
		return nil
	}
	if rm, ok := data.(json.RawMessage); ok {
		return rm
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	return b
}

// ToolOK reports a usable result: content is the human-readable rendering, data
// the structured payload (may be nil).
func ToolOK(kind, content string, data any) ToolMessage {
	return ToolMessage{Kind: kind, Status: StatusOK, Content: content, Data: marshalData(data)}
}

// ToolEmpty reports that the tool ran but produced nothing to show; detail says why.
func ToolEmpty(kind, detail string) ToolMessage {
	return ToolMessage{Kind: kind, Status: StatusEmpty, Detail: detail}
}

// ToolFail reports a failure; detail is the reason, data may carry structured
// error context (e.g. exit_code / stderr).
func ToolFail(kind, detail string, data any) ToolMessage {
	return ToolMessage{Kind: kind, Status: StatusError, Detail: detail, Data: marshalData(data)}
}

// ToolText reports honest prose with no structured payload.
func ToolText(content string) ToolMessage {
	return ToolMessage{Kind: "text", Status: StatusOK, Content: content}
}

// JSON renders the envelope as the string a tool's Execute returns.
func (m ToolMessage) JSON() string {
	b, _ := json.Marshal(m)
	return string(b)
}

// EnvelopeSchema returns the JSON Schema for a tool that emits a ToolMessage —
// the single source of truth for the uniform envelope, so a declared output
// schema can't drift from the actual output. dataSchema is the JSON Schema for
// the tool-specific `data` payload (pass "" when the tool carries only text in
// `content`).
func EnvelopeSchema(dataSchema string) json.RawMessage {
	data := ""
	if dataSchema != "" {
		data = `,"data":` + dataSchema
	}
	return json.RawMessage(`{"type":"object","description":"Uniform tool envelope. status is ok|empty|error; the tool-specific payload is in data; content is the rendered text.","properties":{"kind":{"type":"string"},"status":{"type":"string","enum":["ok","empty","error"]},"content":{"type":"string"},"detail":{"type":"string"}` + data + `}}`)
}

// ParseToolMessage reconstructs an envelope from a tool-result string. ok is
// true only when the string is a well-formed envelope (has a kind AND a valid
// status), so legacy/raw tool output is never mistaken for one.
func ParseToolMessage(raw string) (ToolMessage, bool) {
	var m ToolMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ToolMessage{}, false
	}
	if m.Kind == "" {
		return ToolMessage{}, false
	}
	switch m.Status {
	case StatusOK, StatusEmpty, StatusError:
		return m, true
	}
	return ToolMessage{}, false
}
