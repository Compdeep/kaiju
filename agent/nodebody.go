package agent

import (
	"encoding/json"
	"strings"
)

// NodeBody is the typed output a node produces. It is the source of truth that
// replaces the opaque Node.Result string; a rendered form is kept on
// Node.Result for the frontend/persistence contract during the migration.
//
// The interface is deliberately tiny (deep module, shallow interface): concrete
// bodies wrap the structs each node already computes, and consumers touch only
// these four methods instead of re-parsing JSON out of a string.
type NodeBody interface {
	// Field resolves a dot-path into the body for ${node.<id>.<path>} template
	// references. Returns (value, true) on a hit, (nil, false) otherwise.
	// Struct-backed bodies read their own fields; RawTextBody falls back to
	// parsing its text as JSON, preserving the historical
	// "JSON if it parses, else raw string" contract.
	Field(path string) (any, bool)

	// Evidence renders the body as the text a downstream LLM node (reflector,
	// aggregator) should see. The full, un-truncated form.
	Evidence() string

	// Summary renders a short one-line form for the frontend trace.
	Summary() string

	// Payload is the body's fields, as data, for a stage that has to receive a
	// value rather than read about one. Nil when the body has no structure to
	// give — an opaque string has none.
	//
	// It sits beside Evidence rather than replacing it because the two answer
	// different questions. Evidence is what a stage READS: the content of a
	// page, the output of a command, the text an answer gets written from.
	// Payload is what a stage is GIVEN: the path the page was written to, the
	// exit status, the fields a later step can be wired to.
	//
	// Only Evidence existed, and every stage that needed a value had to find it
	// in prose. A tool's declared fields reached the next NODE, through Field,
	// and reached no STAGE at all — so a path that was correct and on disk was
	// carried to the planner by a sentence a small model retyped, and arrived
	// as a placeholder that resolved to nothing.
	Payload() json.RawMessage
}

// RawTextBody is the fallback body: an opaque string, exactly the pre-refactor
// behavior. Prose tools, plugins, and any producer not yet migrated use it. It
// is also what SetResult wraps a bare string in, so the whole graph carries a
// NodeBody with no producer changes required.
type RawTextBody struct{ Text string }

// RawText wraps a plain string as a NodeBody.
func RawText(s string) RawTextBody { return RawTextBody{Text: s} }

// Field answers a reference against an opaque string: an empty path returns the
// whole string; otherwise it parses the text as JSON and walks the dot-path,
// reporting false when the text is not JSON or the path misses.
//
// What the caller does with a miss is the caller's business. Template
// resolution used to take it as licence to inject the whole string in place of
// the field, and no longer does — a field asked of something that has none is a
// mistake in the step, and it says so.
func (b RawTextBody) Field(path string) (any, bool) {
	if path == "" {
		return b.Text, true
	}
	v, err := extractJSONFieldAny(b.Text, path)
	if err != nil {
		return nil, false
	}
	return v, true
}

// Evidence returns the raw text unchanged.
func (b RawTextBody) Evidence() string { return b.Text }

// Payload is nil: an opaque string has no fields to hand over. A stage reading
// this body gets its text through Evidence and nothing else, which is the
// honest answer for a producer that never declared a shape.
func (b RawTextBody) Payload() json.RawMessage { return nil }

// Summary returns the first non-empty line, matching the frontend's first-line
// fallback for un-typed results.
func (b RawTextBody) Summary() string {
	for _, line := range strings.Split(b.Text, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

// RawBacked supplies the two halves of NodeBody that every JSON-backed body
// answers the same way, so a concrete body only has to write Summary — the one
// part that genuinely differs between a root-cause report, a fix plan and a
// compute result.
//
// Field walks the raw JSON, which is what a ${node.<id>.<path>} reference has
// always meant. Evidence returns it unchanged, because the stage that produced
// it already wrote the text a downstream node should read.
//
// Embed it and keep the parsed struct alongside:
//
//	type HolmesBody struct {
//		RawBacked
//		Out holmesOutput
//	}
//	func (b HolmesBody) Summary() string { … }
//
// A body that needs different behaviour overrides the method it needs —
// ReflectionBody does this for Evidence, to fall back to its summary when there
// is no raw JSON.
type RawBacked struct{ Raw string }

// Field resolves a dot-path into the raw JSON this body kept.
func (b RawBacked) Field(path string) (any, bool) { return RawText(b.Raw).Field(path) }

// Evidence returns the raw JSON unchanged.
func (b RawBacked) Evidence() string { return b.Raw }

// Payload is the raw JSON, when it is JSON. A body backed by prose has no
// fields and says so with nil.
func (b RawBacked) Payload() json.RawMessage {
	if !json.Valid([]byte(b.Raw)) {
		return nil
	}
	return json.RawMessage(b.Raw)
}
