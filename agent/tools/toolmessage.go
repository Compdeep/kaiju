package tools

import "encoding/json"

// ToolStatus is the uniform outcome signal every tool reports, so an edge can
// frame presence / absence / failure the same way regardless of which tool ran.
type ToolStatus string

const (
	StatusOK    ToolStatus = "ok"    // produced usable content
	StatusEmpty ToolStatus = "empty" // ran fine, nothing to show — absence is a real result
	StatusError ToolStatus = "error" // failed

	// StatusUnclassified is a tool that ran and produced output without saying
	// whether it found anything. Not a failure and not an absence: the output is
	// perfectly readable, and only the outcome is undeclared.
	//
	// It is how a tool that emits prose, or its own JSON shape, enters the graph
	// — previously as no envelope at all, which every consumer had to notice by
	// the body's Go type and most silently treated as "fine". This is permanent,
	// not a migration state: plugins and SKILL.md tools produce text by design
	// and will never declare an outcome.
	StatusUnclassified ToolStatus = "unclassified"
)

// ToolMessage is the uniform output envelope every tool emits. The tool-specific
// payload lives verbatim in Data; the envelope adds only the signals an edge
// needs to frame context: what kind of output, did it succeed, is it empty or
// failed and why. It is the "structured communication" layer between tools and
// the graph — deliberately NOT a change to what each tool computes.
type ToolMessage struct {
	Type    string          `json:"type"`              // payload discriminator: search|page|file|listing|command|kv|status|text
	Status  ToolStatus      `json:"status"`            // ok | empty | error | unclassified — the framing signal
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
	return ToolMessage{Type: kind, Status: StatusOK, Content: content, Data: marshalData(data)}
}

// ToolEmpty reports that the tool ran but produced nothing to show; detail says why.
func ToolEmpty(kind, detail string) ToolMessage {
	return ToolMessage{Type: kind, Status: StatusEmpty, Detail: detail}
}

// ToolFail reports a failure; detail is the reason, data may carry structured
// error context (e.g. exit_code / stderr).
func ToolFail(kind, detail string, data any) ToolMessage {
	return ToolMessage{Type: kind, Status: StatusError, Detail: detail, Data: marshalData(data)}
}

// ToolText reports honest prose with no structured payload.
func ToolText(content string) ToolMessage {
	return ToolMessage{Type: "text", Status: StatusOK, Content: content}
}

/*
 * ToolUnclassified wraps output from a tool that did not declare an outcome.
 * desc: The producer's text, kept verbatim, with the outcome left undeclared
 *       rather than assumed. Used where a result enters the graph without an
 *       envelope of its own — prose tools, plugins, SKILL.md wrappers, and any
 *       tool not yet migrated.
 *
 *       Deliberately not ToolOK. Calling it ok asserts the tool found something,
 *       which is the fabrication the outcome signal exists to prevent; calling it
 *       empty asserts it found nothing, which is the same mistake reversed.
 * param: content - what the tool returned, unchanged.
 * return: the envelope.
 */
func ToolUnclassified(content string) ToolMessage {
	return ToolMessage{Type: "text", Status: StatusUnclassified, Content: content}
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
//
// The wrapper is marked "x-envelope" so a reader can find the tool's own schema
// underneath it. Anything describing a tool to a model wants that inner schema
// and not this one: the envelope is plumbing, and a planner shown five generic
// keys learns nothing about what the tool returns. See PayloadSchema.
func EnvelopeSchema(dataSchema string) json.RawMessage {
	data := ""
	if dataSchema != "" {
		data = `,"data":` + dataSchema
	}
	return json.RawMessage(`{"type":"object","x-envelope":true,"description":"Uniform tool envelope. status is ok|empty|error; the tool-specific payload is in data; content is the rendered text.","properties":{"type":{"type":"string"},"status":{"type":"string","enum":["ok","empty","error"]},"content":{"type":"string"},"detail":{"type":"string"}` + data + `}}`)
}

// ParseToolMessage reconstructs an envelope from a tool-result string. ok is
// true only when the string is a well-formed envelope (has a type AND a valid
// status), so legacy/raw tool output is never mistaken for one.
//
// The field was called "kind" until it was renamed, and a remote plugin host is
// third-party code built against whichever name it saw. Reading both costs one
// unmarshal of a string that already parsed; writing is always the new name.
func ParseToolMessage(raw string) (ToolMessage, bool) {
	var m ToolMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ToolMessage{}, false
	}
	if m.Type == "" {
		var legacy struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal([]byte(raw), &legacy) == nil {
			m.Type = legacy.Kind
		}
	}
	if m.Type == "" {
		return ToolMessage{}, false
	}
	switch m.Status {
	case StatusOK, StatusEmpty, StatusError, StatusUnclassified:
		return m, true
	}
	return ToolMessage{}, false
}

// PayloadSchema returns the tool's own output schema: the payload under an
// envelope wrapper, or the schema as given when it is not wrapped.
//
// Three readers describe a tool's output — the planner's tool index, the
// plan-time check on ${step.N.field} references, and the chain hints — and each
// wants the tool's fields, never the envelope's. Only one of them used to
// descend, so the other two told a planner that every tool returns the same
// five keys.
//
// nil means the tool declares no payload at all: it carries text in content and
// there is nothing to describe.
func PayloadSchema(schema json.RawMessage) json.RawMessage {
	var root map[string]json.RawMessage
	if json.Unmarshal(schema, &root) != nil {
		return schema
	}
	var wrapped bool
	if raw, ok := root["x-envelope"]; ok {
		_ = json.Unmarshal(raw, &wrapped)
	}

	var props map[string]json.RawMessage
	if raw, ok := root["properties"]; ok {
		_ = json.Unmarshal(raw, &props)
	}
	data, hasData := props["data"]

	// The mark is the answer where it is present. Where it is not — a schema
	// written by hand around the same shape — a top-level "data" object is the
	// same wrapper by another route, which is the rule referencePaths already
	// used before this function existed.
	if wrapped {
		if !hasData {
			return nil
		}
		return json.RawMessage(data)
	}
	if hasData {
		var obj map[string]json.RawMessage
		if json.Unmarshal(data, &obj) == nil {
			if _, isObject := obj["properties"]; isObject {
				return json.RawMessage(data)
			}
		}
	}
	return schema
}
