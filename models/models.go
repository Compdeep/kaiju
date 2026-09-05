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
	// A model that thinks BY DEFAULT counts as one: kaiju never sends a thinking
	// or reasoning parameter (see agent/llm/anthropic.go and client.go), so the
	// provider's default is what we get.
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
	// Vision reports whether the model accepts image input.
	Vision bool `json:"vision,omitempty"`
	// Chat marks a model suited to the chat lane (conversation / roleplay tunes).
	Chat bool `json:"chat,omitempty"`
	// Roles lists the lanes this model is suitable for: answer, planner, executor,
	// router, chat, vision. The UI filters each lane's picker by role.
	Roles []string `json:"roles,omitempty"`
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
 * ForcedSmallCall returns the models fit for a lane that forces a SMALL tool
 * call — the router's 96 tokens, the executor's classifiers.
 * desc: Everything that can emit such a call, whether or not it reasons by
 *       default, because those lanes turn reasoning off before they send —
 *       always, for every model, with nothing an operator can set (agent/ask.go).
 *
 *       It briefly excluded thinking models, and the measurement killed that.
 *       Run against three real preflight prompts from a live deployment with
 *       reasoning off, the three best models were all reason-capable:
 *       qwen3.6-35b-a3b at 155 tokens and 1.7s, qwen3.5-35b-a3b at 149 and 2.4s,
 *       deepseek-v4-flash-vision-exp at 238 and 2.9s. The non-thinking model
 *       that had been the default took 41.9s and was cut at the cap on one of
 *       the three.
 *
 *       So the exclusion did not protect the lane; it hid the models that won
 *       it. What it was written for was a time when kaiju sent no reasoning
 *       parameter and got the provider's default, which on this generation is
 *       ON. That premise is gone.
 * return: a fresh slice, in catalog order.
 */
func ForcedSmallCall() []Info {
	out := make([]Info, 0, len(all))
	for _, m := range all {
		if m.Tools && m.ToolCallOK {
			out = append(out, m)
		}
	}
	return out
}

/*
 * RouterFit returns the models a router picker should offer.
 * desc: Everything that can emit a small forced call, whether or not it reasons
 *       by default, because the router lane turns reasoning off before it sends
 *       — always, for every model, with nothing an operator can set.
 *
 *       This is the exception to the rule ForcedSmallCall states, and it exists
 *       because the rule outlived its evidence. Excluding thinkers made sense
 *       while kaiju sent no reasoning parameter and got the provider's default,
 *       which on the current generation is ON. Now the lane forces it off, and
 *       every model released since early 2026 is reason-capable — so the
 *       exclusion no longer protects the lane, it just hides everything modern
 *       from the one picker where cost is irrelevant and quality is the only
 *       question. The router runs once a turn at 96 tokens.
 *
 *       The executor keeps the stricter list. It has the same reasoning forced
 *       off, so the same argument applies to it; what differs is that the
 *       executor has good non-thinking choices and the router, on the current
 *       catalogue, does not.
 * return: a fresh slice, in catalog order.
 */
func RouterFit() []Info {
	return ToolSafe()
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
