package toolfind

import (
	"strings"
	"unicode"
)

// Turning text into the terms an index is built from and searched by.
//
// The same function has to run over a tool's document and over the objective,
// or a term written one way in one and another way in the other never meets
// itself. So there is one tokenizer here and no second path.

// tokenize returns the lowercase terms of s.
//
// Runs of letters and digits are terms. A run written in a script that puts no
// spaces between words becomes its overlapping character pairs instead, which
// is what the message index does for the same reason — those scripts have no
// word boundaries to split on, and a whole sentence would otherwise arrive as
// a single term that only an identical sentence could match.
func tokenize(s string) []string {
	var out []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			out = append(out, word.String())
			word.Reset()
		}
	}
	var unspacedRun []rune
	flushUnspaced := func() {
		switch len(unspacedRun) {
		case 0:
		case 1:
			out = append(out, string(unspacedRun))
		default:
			for i := 0; i+1 < len(unspacedRun); i++ {
				out = append(out, string(unspacedRun[i:i+2]))
			}
		}
		unspacedRun = unspacedRun[:0]
	}

	for _, r := range s {
		switch {
		case unspaced(r):
			flush()
			unspacedRun = append(unspacedRun, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushUnspaced()
			word.WriteRune(unicode.ToLower(r))
		default:
			flush()
			flushUnspaced()
		}
	}
	flush()
	flushUnspaced()
	return out
}

// unspaced reports whether a character is written in a script that puts no
// spaces between words.
func unspaced(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}
