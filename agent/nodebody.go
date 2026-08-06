package agent

import "strings"

// NodeBody is the typed output a node produces. It is the source of truth that
// replaces the opaque Node.Result string; a rendered form is kept on
// Node.Result for the frontend/persistence contract during the migration.
//
// The interface is deliberately tiny (deep module, shallow interface): concrete
// bodies wrap the structs each node already computes, and consumers touch only
// these three methods instead of re-parsing JSON out of a string.
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
}

// RawTextBody is the fallback body: an opaque string, exactly the pre-refactor
// behavior. Prose tools, plugins, and any producer not yet migrated use it. It
// is also what SetResult wraps a bare string in, so the whole graph carries a
// NodeBody with no producer changes required.
type RawTextBody struct{ Text string }

// RawText wraps a plain string as a NodeBody.
func RawText(s string) RawTextBody { return RawTextBody{Text: s} }

// Field preserves the legacy template contract: an empty path returns the whole
// string; otherwise it tries to parse the text as JSON and walk the dot-path,
// returning (nil, false) when the text is not JSON or the path misses — the
// caller then degrades to the raw string, exactly as resolveTemplateField does
// today.
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
