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
	Thinking bool `json:"thinking"`
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
func load() []Info {
	var cat catalog
	if err := json.Unmarshal(modelsJSON, &cat); err != nil {
		log.Printf("[models] catalog: parse failed, catalog empty: %v", err)
		return nil
	}
	return cat.Models
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
 * desc: The planner pins tool_choice to one named tool, and two kinds of model
 *       cannot honour that: one in thinking mode (providers reject an object
 *       tool_choice outright, or the model spends its budget reasoning and
 *       never calls), and one that simply does not emit small forced calls
 *       reliably. This is the filter a model picker wants — it is also what
 *       drops the roleplay tunes, which carry no tool support at all.
 * return: a fresh slice, in catalog order.
 */
func ToolSafe() []Info {
	out := make([]Info, 0, len(all))
	for _, m := range all {
		if !m.Thinking && m.ToolCallOK {
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
