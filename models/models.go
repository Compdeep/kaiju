/*
 * Package models is kaiju's model catalog: which LLMs exist, what each one can
 * do, and which lanes it is fit for.
 *
 * It lives here, outside internal/, because it is not only the daemon's
 * business. An application that embeds this engine as a library needs the same
 * list to build its own model picker, and Go's internal/ rule put it out of
 * reach. Two hand-maintained copies of this list is how such a picker ends up
 * offering a model id the engine cannot call: the id no longer exists at the
 * provider, or the model refuses a forced tool call, and either surfaces to a
 * user as a failed run with nothing pointing back at the picker.
 *
 * internal/configapi still serves it over HTTP for kaiju's own UI; it now reads
 * from here rather than owning it.
 */
package models

import (
	_ "embed"
	"encoding/json"
	"log"
)

/*
 * Info describes one supported LLM.
 * desc: The model id, display name, provider, limits, and the capability flags
 *       each lane's picker filters on.
 */
type Info struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Family   string `json:"family,omitempty"`  // e.g. "qwen3", "gpt-4.1", "gemini"
	Params   string `json:"params,omitempty"`  // e.g. "30B-A3B", "8B", "235B-A22B"
	Version  string `json:"version,omitempty"` // e.g. "2507", "3.3"
	Provider string `json:"provider"`
	Context  string `json:"context,omitempty"`
	// ContextTokens and MaxOutputTokens are the same limits the provider
	// publishes, in tokens, for arithmetic — Context above is a label for the
	// picker and cannot be computed with. Zero means the catalog does not know,
	// and a caller must keep whatever cap it would otherwise have used.
	ContextTokens   int `json:"context_tokens,omitempty"`
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// Thinking marks a reasoning model that emits hidden reasoning tokens before
	// its output. Fine for open-ended generation (answer/chat), but it starves on
	// small forced tool calls — see ToolCallOK.
	//
	// A model that thinks BY DEFAULT counts as one. On the lanes that make a
	// small forced call kaiju sends reasoning off (agent/ask.go), so there the
	// answer that matters is ReasoningOptional; on every other lane no parameter
	// is sent and the provider's default is what we get.
	//
	// A pointer so that ABSENT is not the same as false, and it has to be a
	// pointer because of which way false leans. Leave tool_call_ok out and the
	// entry decodes as "cannot emit a small forced call", which excludes it from
	// the lanes that make one: wrong, and safe, and noticed the first time
	// somebody looks for it in a picker. Leave THIS out and the entry decodes as
	// "does not reason before answering", which is the permissive answer — the
	// model is offered for the router's 16-token forced call and fails there the
	// way docs/router-model-bench.md describes, silently, as under-escalation.
	//
	// So the dangerous default is the one you get by typing nothing, and load()
	// refuses an entry that does not say. Read it through Thinks().
	Thinking *bool `json:"thinking"`
	// ReasoningOptional reports whether the reasoning phase can be switched OFF
	// by request — OpenRouter's per-model `reasoning.mandatory` being false. It
	// is the question the executor and router pickers actually need to ask: they
	// send reasoning off (agent/ask.go), so a model that merely THINKS BY
	// DEFAULT is fine there, and only a model that cannot stop is not.
	//
	// Not a pointer, and the default leans the other way from Thinking on
	// purpose. Absent decodes as "cannot be switched off", which keeps an
	// undeclared entry out of the small-forced-call lanes — the same safe
	// exclusion ToolCallOK gives.
	//
	// A model with no reasoning phase at all has both false: nothing to switch.
	ReasoningOptional bool `json:"reasoning_optional"`
	// Tools reports whether the model can call tools at all.
	Tools bool `json:"tools"`
	// ToolCallOK reports whether the model reliably emits a SMALL forced tool call
	// (router @16 tok / executor @256 tok). This is the router/executor-lane gate:
	// a thinking model usually fails it (burns the budget reasoning → no tool call).
	ToolCallOK bool `json:"tool_call_ok"`
	// Verified is true when ToolCallOK was measured by the bench (docs/router-model-bench.md)
	// rather than inferred from the model family.
	Verified bool `json:"verified"`
	// Available reports whether this model's provider is configured with a key.
	// Computed at serve time by whoever serves the catalog, not stored in it.
	Available bool `json:"available"`
	// FitsSmallCall is FitsForcedSmallCall as a value, so a settings page reads
	// one boolean instead of re-deriving the rule from Tools, ToolCallOK,
	// Thinking and ReasoningOptional. Two of them did, in two languages, and
	// they disagreed with the daemon for a day.
	//
	// Derived, never read from models.json: load() sets it, so it is right on
	// every path that serves the catalog rather than only the one that
	// remembered. Marshalled unconditionally — absent, a client cannot tell a
	// model that does not fit from a server too old to say.
	FitsSmallCall bool `json:"fits_small_call"`
	// Vision reports whether the model accepts image input.
	Vision bool `json:"vision,omitempty"`
	// Chat marks a model suited to the chat lane (conversation / roleplay tunes).
	Chat bool `json:"chat,omitempty"`
	// Roles lists the lanes this model is suitable for: answer, planner, executor,
	// router, chat, vision. The UI filters each lane's picker by role.
	Roles []string `json:"roles,omitempty"`
	// ReasoningEfforts are the effort values this model actually acts on, and it
	// is a MEASUREMENT rather than a capability the provider advertises.
	//
	// Every provider accepts the parameter; not every model does anything with
	// it. On one prompt, deepseek-v4-pro reasoned for 916 tokens by default and
	// 571 at "low" — it listens. qwen3.6-35b-a3b returned 1,498 and 1,548 —
	// it does not. Neither errored, so asking the provider tells you nothing.
	//
	// Empty is the safe default and the honest one: no control is offered for a
	// model nobody has measured, rather than one that appears to do something.
	// Nothing here is inferred from the family, and the catalog now shows why:
	// deepseek-v4-pro-0813 acts on effort while deepseek-v4-flash-0731 acts on
	// it BACKWARDS, and glm-5.3 acts on it while glm-5.3-flash does not. See
	// Verified for where that lesson was learned first, and
	// docs/reasoning-effort-bench.md for what each entry here was measured at.
	//
	// The values are the providers' vocabulary and not ours: minimal, low,
	// medium, high, xhigh, max. No model takes all six, and glm-5.2 takes
	// neither low nor medium.
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
	// ReasoningBudget reports whether a reasoning budget in tokens is honoured
	// as a budget. Two models in the catalog do: qwen3.7-flash and qwen3.8-flash
	// return exactly 256 and exactly 4,096 reasoning tokens when that is what
	// they are allowed. False is the default for the same reason as above.
	//
	// Measuring this needs a prompt the budget BINDS on. On an easy one these
	// models reason for about 130 tokens anyway, and a 128-token allowance
	// cannot be told apart from their own default — which is how qwen3.8-flash
	// was first recorded as ignoring it.
	//
	// This is a statement about the WIRE as much as the model. Anthropic takes a
	// budget natively, as budget_tokens, and claude-opus-5 reached over
	// OpenRouter ignores it completely — 113 tokens spent whether 128 or 4,096
	// were asked for. Every entry in models.json is provider "openrouter", so
	// that is the wire these values describe. A deployment pointed at
	// api.anthropic.com is a different one, and unmeasured.
	ReasoningBudget bool `json:"reasoning_budget,omitempty"`
	// Pace is how long this model takes to answer compared with the rest, and
	// it lengthens the deadlines rather than describing them: "slow" is given
	// half again as long and "very slow" twice as long. Absent is ordinary,
	// which is what every entry says unless it has been measured otherwise.
	//
	// A deadline is one number for every model, and a model materially slower
	// than the rest is cut off by it while it is working. That costs a second
	// call to recover and produces a worse answer than waiting would have.
	//
	// Measured, per model, and only where the evidence is a model's own pace
	// rather than one slow afternoon at a provider. What each entry was set
	// from is in docs/model-pace.md, with the two benches it was read out of.
	//
	// A median cannot answer this. kimi-k2.6 answers in 22 seconds half the
	// time and passed the 120-second deadline on 5 of its 26 live calls, so
	// what marks a model is its tail.
	//
	// The word describes the model's speed and not its size: a large model can
	// answer quickly and a small reasoning model can take four minutes.
	Pace string `json:"pace,omitempty"`
}

// The pace values, and what each is worth in time. Ours, not a provider's.
const (
	PaceSlow     = "slow"      // half again as long
	PaceVerySlow = "very slow" // twice as long
)

// paceMultiple is what each pace is worth. A value not in here is ordinary.
var paceMultiple = map[string]float64{
	PaceSlow:     1.5,
	PaceVerySlow: 2,
}

/*
 * DeadlineMultiple is what this model's deadlines are multiplied by.
 * desc: 1 for a model whose pace has not been measured, which is every entry
 *       that says nothing. Never below 1: this lengthens a deadline and never
 *       shortens one, so a wrong entry costs waiting rather than an answer.
 * return: the multiplier, 1 or greater.
 */
func (i Info) DeadlineMultiple() float64 {
	if m, ok := paceMultiple[i.Pace]; ok {
		return m
	}
	return 1
}

// catalog is the on-disk shape of models.json.
type catalog struct {
	Version int    `json:"version"`
	Models  []Info `json:"models"`
}

// The catalog itself. Operators may override it from data_dir/models.json in a
// future revision; for now the embedded JSON is the single source of truth
// (edit models.json).
//
//go:embed models.json
var modelsJSON []byte

var all = load()

// load parses the embedded catalog. On a malformed file it returns an empty
// list — callers then serve nothing — rather than panicking at init.
//
// An entry that does not declare `thinking` is dropped and named. The catalog is
// embedded, so this can only be reached by editing models.json and rebuilding:
// the person who caused it is the person reading the log, and the fix is one
// word. Dropping the entry rather than defaulting it is the point — a default
// here is the silent-permissive case this field exists to prevent.
func load() []Info {
	var cat catalog
	if err := json.Unmarshal(modelsJSON, &cat); err != nil {
		log.Printf("[models] catalog: parse failed, catalog empty: %v", err)
		return nil
	}
	out := cat.Models[:0]
	for _, m := range cat.Models {
		if m.Thinking == nil {
			log.Printf("[models] catalog: %q does not declare \"thinking\", so it is dropped — "+
				"an entry that omits it would read as a model that does not reason before answering, "+
				"which is the answer that gets it offered for a forced tool call", m.ID)
			continue
		}
		if _, ok := paceMultiple[m.Pace]; m.Pace != "" && !ok {
			// Not dropped: the entry is usable and only its allowance is in
			// question, and the value it falls back to is the SHORT one. A typo
			// here costs a deadline that was already the default rather than a
			// model that vanishes from every picker.
			log.Printf("[models] catalog: %q says pace %q, which is not one of %q or %q — "+
				"it is given the ordinary deadlines", m.ID, m.Pace, PaceSlow, PaceVerySlow)
			m.Pace = ""
		}
		m.FitsSmallCall = m.FitsForcedSmallCall()
		out = append(out, m)
	}
	return out
}

/*
 * All returns every model in the catalog.
 * desc: A fresh copy each call, so a caller stamping per-request fields onto
 *       the entries (Available) cannot scribble on the shared catalog.
 * return: the catalog, in file order.
 */
func All() []Info {
	out := make([]Info, len(all))
	copy(out, all)
	return out
}

/*
 * ToolSafe returns the models that can drive a lane which forces a tool call.
 * desc: Two fields decide it: the model can call tools at all, and it emits a
 *       SMALL forced call reliably. That is what the name claims and what the
 *       fields measure.
 *
 *       Thinking used to be a third term, and it excluded every reasoning model
 *       outright. Two things broke that. It was measured false in one direction
 *       — qwen3-32b reasons AND returned a complete plan three times out of
 *       three — and on every model line released since early 2026 reasoning is
 *       a SWITCH the request turns off, not a property the model is stuck with.
 *       Keeping the term hid most of the current catalogue from every picker,
 *       including the model an installation was already configured to run,
 *       which is how a settings page ends up showing an empty dropdown.
 *
 *       What a thinking model costs a lane is still real and still said: the
 *       lane warnings name it at startup (internal/config), and a picker can
 *       read Thinks() to mark it. This decides what is OFFERED; it does not
 *       decide what is wise.
 * return: a fresh slice, in catalog order.
 */
func ToolSafe() []Info {
	out := make([]Info, 0, len(all))
	for _, m := range all {
		if m.Tools && m.ToolCallOK {
			out = append(out, m)
		}
	}
	return out
}

/*
 * Thinks reports whether this model reasons before it answers.
 * desc: The field is a pointer so an entry that does not declare it can be told
 *       from one that declares false — see Thinking. load() drops the undeclared
 *       ones, so by the time a caller has an Info from this package the pointer
 *       is set; this reads false for a zero Info built anywhere else rather than
 *       dereferencing nil.
 * return: true when the model emits hidden reasoning tokens.
 */
func (i Info) Thinks() bool { return i.Thinking != nil && *i.Thinking }

/*
 * ReasoningLocked reports a model that reasons and cannot be told not to.
 * desc: The complaint the lanes that force a small call actually have. Those
 *       lanes send reasoning off on every call (agent/ask.go) and a hybrid model
 *       obeys, so reasoning BY DEFAULT is not a fault there — and reading it as
 *       one excludes most of the catalog, every Qwen since 3.5 included.
 *
 *       Split from FitsForcedSmallCall because the two callers need different
 *       granularity: a picker wants one answer, and the startup lane warning
 *       has to say WHICH fault it found, in different words for each.
 * return: true when no request can stop this model reasoning.
 */
func (i Info) ReasoningLocked() bool { return i.Thinks() && !i.ReasoningOptional }

/*
 * FitsForcedSmallCall reports whether a lane that pins one tool inside a small
 * reply budget can rely on this model.
 * desc: The single definition of that rule. It was written out four times —
 *       here, both startup lane checks, and both settings pages — so changing
 *       what disqualifies a model meant locating all four. One was missed for a
 *       day, which is how a picker came to offer models the daemon then warned
 *       about.
 *
 *       Computed in load() rather than read from models.json, so every path that
 *       serves the catalog carries it without having to remember to. That is
 *       also why the two settings pages read one boolean instead of deriving it
 *       from three flags each.
 * return: true when the model can be asked for one small forced call.
 */
func (i Info) FitsForcedSmallCall() bool {
	return i.Tools && i.ToolCallOK && !i.ReasoningLocked()
}

/*
 * ForcedSmallCall returns the models fit for a lane that forces a SMALL tool
 * call — the router at 96 tokens, the executor's classifiers.
 * desc: ToolSafe minus the ones that reason before answering AND cannot be told
 *       to stop. Reasoning is excluded here and nowhere else: on the reasoning
 *       lane it earns its cost, and on answer and chat it is simply better. It
 *       is only in a small forced call that it has no upside — the reasoning
 *       consumes the budget the call was to fill, and the reply arrives empty or
 *       unparseable.
 *
 *       Thinking BY DEFAULT is not the test, because the engine sends reasoning
 *       off on these lanes (see agent/ask.go) and the model obeys. The test is
 *       whether it can be switched off at all: a mandatory-reasoning model
 *       reasons through the budget no matter what is sent. Reading the softer
 *       question cost the picker every modern hybrid model, which is most of
 *       them — every Qwen since 3.5 ships one line that does both.
 *
 *       This remains the second of two doors rather than the only one. A picker
 *       that offered a thinking model here would be offering a choice the engine
 *       then overrides, which is worse than not offering it: the operator would
 *       have picked a model for a property it is not allowed to use.
 * return: a fresh slice, in catalog order.
 */
func ForcedSmallCall() []Info {
	out := make([]Info, 0, len(all))
	for _, m := range all {
		if m.FitsForcedSmallCall() {
			out = append(out, m)
		}
	}
	return out
}

/*
 * Limits reports what a model can take in and give back, in tokens.
 * desc: Reads the two numeric fields of the catalog entry. Both are zero when
 *       the id is not in the catalog, or when the catalog carries no numbers
 *       for it — the caller then keeps the cap it would otherwise have used.
 *       Suitable as agent.Config.Limits.
 * param: id - the model id as configured for a lane, e.g. "openai/gpt-4.1".
 * return: the context window and the largest reply the provider will produce.
 */
func Limits(id string) (contextTokens, maxOutputTokens int) {
	for _, m := range all {
		if m.ID == id {
			return m.ContextTokens, m.MaxOutputTokens
		}
	}
	return 0, 0
}

/*
 * Find returns the catalog entry for a model id.
 * desc: The catalog is curated rather than exhaustive, so false means the list
 *       has never heard of the id — a self-hosted model, or one newer than the
 *       list — and not that the id is wrong. A caller deciding whether to
 *       complain has to tell those two apart, which is why this reports both
 *       the entry and whether there was one.
 * param: id - the model id as configured for a lane, e.g. "qwen/qwen3-32b".
 * return: the entry and true, or the zero Info and false.
 */
func Find(id string) (Info, bool) {
	for _, m := range all {
		if m.ID == id {
			return m, true
		}
	}
	return Info{}, false
}
