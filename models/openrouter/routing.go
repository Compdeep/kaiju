/*
 * Package openrouter says which OpenRouter providers kaiju will and will not be
 * routed to.
 *
 * OpenRouter serves one model id from several hosts, and they are not
 * interchangeable. Measured on qwen3.6-35b-a3b: through one host, reasoning on,
 * five replies of five came back wrapped in a markdown fence; through others,
 * none did. The engine reads a fenced plan anyway — see parseExecutivePayload —
 * but parsing around a host is a worse answer than not being sent to it.
 *
 * Two lists, because they are different tools. The blacklist drops the host you
 * distrust and leaves the rest as fallback. The whitelist removes fallback
 * altogether: name a host and every other one is refused, so that host being
 * down becomes a failed call rather than a slower answer from someone else.
 * Reach for the blacklist unless something other than quality is forcing the
 * choice — a data-residency rule, say.
 *
 * The lists live beside the model catalog rather than in kaiju.json, for the
 * same reason it does: they are facts about the providers, not settings for a
 * deployment, and every deployment wants the same answer. Editing them means a
 * rebuild, as with models.json.
 */
package openrouter

import (
	_ "embed"
	"encoding/json"
	"log"
)

//go:embed blacklist.json
var blacklistJSON []byte

//go:embed whitelist.json
var whitelistJSON []byte

// list is the shape of both files. The comment field is there to be written and
// never read; it carries the slug rules to whoever opens the file next.
type list struct {
	Comment   string   `json:"_comment"`
	Providers []string `json:"providers"`
}

var (
	blocked = load("blacklist.json", blacklistJSON)
	allowed = load("whitelist.json", whitelistJSON)
)

/*
 * load parses one list.
 * desc: A malformed file yields no entries and says so. That is the safe way to
 *       fail for both lists and for opposite reasons: an unreadable blacklist
 *       that blocked nothing costs a bad host, while an unreadable whitelist
 *       read as "allow none" would refuse every provider and end the run.
 *       Empty means "say nothing about routing", which is right for both.
 * param: name - the file, for the log line.
 * param: raw - its contents.
 * return: the slugs, or nil.
 */
func load(name string, raw []byte) []string {
	var l list
	if err := json.Unmarshal(raw, &l); err != nil {
		log.Printf("[openrouter] %s: parse failed, no providers %s: %v",
			name, map[bool]string{true: "blocked", false: "allowed"}[name == "blacklist.json"], err)
		return nil
	}
	out := make([]string, 0, len(l.Providers))
	for _, p := range l.Providers {
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

/*
 * Blocked returns the providers OpenRouter must skip.
 * return: a fresh copy, or nil when the list is empty.
 */
func Blocked() []string { return copyOf(blocked) }

/*
 * Allowed returns the only providers OpenRouter may use.
 * return: a fresh copy, or nil when the list is empty, which means "no
 *         restriction" rather than "allow none".
 */
func Allowed() []string { return copyOf(allowed) }

// copyOf hands out a copy so a caller stamping the slice onto a request cannot
// scribble on the parsed list — the same reason models.All does.
func copyOf(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
