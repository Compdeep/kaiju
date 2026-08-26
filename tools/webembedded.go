package tools

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Reading a page that renders itself.
//
// A page built by a JavaScript framework ships as a shell: the markup holds a
// nav bar and an empty root element, and everything a reader wants arrives
// afterwards. Readability finds nothing in it, correctly — measured on two of
// them, 20KB and 47KB of HTML holding 48 characters of text between them.
//
// But the state those frameworks hydrate FROM is usually in the document
// already, in a script tag, as JSON. Next.js writes __NEXT_DATA__, Nuxt writes
// __NUXT__, and many sites emit application/ld+json for search engines. Reading
// that needs no browser and no dependency — a regular expression and
// encoding/json — and on the same two pages it recovers 21KB.
//
// It is a LAST resort, not a first one. It runs only where the page has already
// produced nothing, so a page that extracts normally is never touched by it: the
// worst it can do is turn an empty result into a different empty result.
//
// It is not a way past bot protection. A page that answers with a challenge has
// no embedded state either, and nothing here changes that.

// embeddedScripts finds JSON carried in a script tag. Ordered: the framework
// state first, since it holds the page's own data, then the descriptive blocks.
var embeddedScripts = []*regexp.Regexp{
	regexp.MustCompile(`(?is)<script[^>]+id=["']__NEXT_DATA__["'][^>]*>(.*?)</script>`),
	regexp.MustCompile(`(?is)<script[^>]+type=["']application/json["'][^>]*>(.*?)</script>`),
	regexp.MustCompile(`(?is)<script[^>]+type=["']application/ld\+json["'][^>]*>(.*?)</script>`),
	regexp.MustCompile(`(?is)window\.__NUXT__\s*=\s*(\{.*?\})\s*;?\s*</script>`),
}

/*
 * embeddedJSON returns the state a page carries for its own scripts to read.
 * desc: Only what parses as JSON, and only what is big enough to be data rather
 *       than configuration — a 190-byte empty Next.js shell is a page whose
 *       content arrives later over the network, and returning it would claim to
 *       have read a page that has not loaded.
 *
 *       Blocks are joined newline-separated, largest first, so a reader that
 *       gets only the beginning gets the substantial one.
 * param: html - the page as fetched.
 * param: min - the smallest block worth returning, in bytes.
 * return: the JSON found, or "" when the page carries none worth reading.
 */
func embeddedJSON(html string, min int) string {
	var blocks []string
	seen := map[string]bool{}
	for _, re := range embeddedScripts {
		for _, m := range re.FindAllStringSubmatch(html, -1) {
			raw := strings.TrimSpace(m[1])
			if len(raw) < min || seen[raw] {
				continue
			}
			// Parsed rather than pattern-matched: a script tag holding something
			// that merely looks like JSON is not data, and handing a downstream
			// stage a fragment would be worse than handing it nothing.
			var probe any
			if json.Unmarshal([]byte(raw), &probe) != nil {
				continue
			}
			seen[raw] = true
			blocks = append(blocks, raw)
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	// Largest first. Every cap between here and a model cuts from the end.
	for i := 1; i < len(blocks); i++ {
		for j := i; j > 0 && len(blocks[j]) > len(blocks[j-1]); j-- {
			blocks[j], blocks[j-1] = blocks[j-1], blocks[j]
		}
	}
	return strings.Join(blocks, "\n")
}
