package llm

// What this package needs to know about a model, and one way to be told it.
//
// The knowledge is the application's: kaiju ships a catalog, an embedding
// application may have its own, and a self-hosted endpoint may be in neither.
// So it arrives as a lookup rather than a table, and a model nobody can answer
// for is the ordinary case rather than an error.
//
// One lookup, not five. Every question below is answered from the same catalog
// entry, and a seam per question is a wide interface on a deep module — the
// shape docs/design-notes.md names as the thing to avoid.

// ModelFacts is what a model can take, what it can give back, and what it does
// with an instruction to think.
//
// A zero value means "nothing known", which every behaviour built on this must
// treat as "change nothing". That is the contract an application relies on when
// it supplies no catalog at all.
type ModelFacts struct {
	// ContextTokens is the whole window: prompt and reply together.
	ContextTokens int
	// MaxOutputTokens is the most this model will write in one reply. Some
	// providers reject a max_tokens above it rather than trimming, so this is
	// correctness and not only tidiness.
	MaxOutputTokens int
	// Thinking is what the model does with a reasoning instruction.
	Thinking Thinking
}

// Thinking is what a model does when asked to reason, or asked not to.
//
// Every field is a MEASUREMENT rather than a capability the provider
// advertises. Every provider accepts every reasoning parameter and none errors
// on any of them, so a model that ignores one answers exactly like a model that
// acts on it — see docs/reasoning-effort-bench.md for how these were obtained.
type Thinking struct {
	// Default reports whether the model reasons unless told otherwise. On every
	// model line released since early 2026 this is true: the separate -instruct
	// variants stopped, and one hybrid model took their place.
	Default bool
	// Optional reports whether the reasoning phase can be switched OFF by
	// request. False is the safe default — it keeps an undeclared model out of
	// the calls that cannot afford to think.
	Optional bool
	// Efforts are the effort values this model was measured to act on. Empty
	// means nothing measured, and nothing is asked of it.
	Efforts []Effort
	// Budget reports whether a thinking budget in tokens is honoured AS a
	// budget — the model returning roughly what it was allowed rather than
	// whatever it wanted.
	Budget bool
	// Pace is the multiplier on an ordinary deadline. 1, or 0 for unknown,
	// means the ordinary one. Never below 1: a measurement lengthens a deadline
	// and never shortens one, so a wrong entry costs waiting rather than an
	// answer.
	Pace float64
}

// Catalog answers for one model. The second return is false for a model it does
// not carry, which is not an error — it is a self-hosted endpoint, or a model
// released since the catalog was written.
type Catalog func(model string) (ModelFacts, bool)

/*
 * Catalog tells a client what its models can do.
 * desc: Set beside Transport and for the same reason: a per-call decision is a
 *       decision somebody forgets to make.
 *
 *       Supersedes Limits and Thinks, which remain for callers already using
 *       them and are read only when no catalog is set.
 * param: fn - the lookup, or nil to leave every request as its caller wrote it.
 * return: the client, so this reads as part of construction.
 */
func (c *Client) Catalog(fn Catalog) *Client {
	c.catalog = fn
	return c
}

/*
 * facts is what this client knows about the model a request will reach.
 * desc: The catalog where one is set, the older narrow lookups where they are,
 *       and nothing at all otherwise — which is what makes every behaviour
 *       built on this inert for an application that supplies neither.
 * param: model - the model id; empty falls back to the client's own.
 * return: the facts, and whether anything is known.
 */
func (c *Client) facts(model string) (ModelFacts, bool) {
	if c == nil {
		return ModelFacts{}, false
	}
	if model == "" {
		model = c.model
	}
	if model == "" {
		return ModelFacts{}, false
	}
	if c.catalog != nil {
		return c.catalog(model)
	}

	// The narrow lookups, as the part of a ModelFacts they can answer. An
	// application that set Limits gets the reply sized and nothing else, which
	// is exactly what it got before this file existed.
	var f ModelFacts
	known := false
	if c.limits != nil {
		if ctxTok, maxOut := c.limits(model); ctxTok > 0 || maxOut > 0 {
			f.ContextTokens, f.MaxOutputTokens = ctxTok, maxOut
			known = true
		}
	}
	if c.thinks != nil && c.thinks(model) {
		f.Thinking.Default = true
		known = true
	}
	return f, known
}
