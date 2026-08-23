package db

import (
	"database/sql/driver"
	"strings"
	"unicode"
	"unicode/utf8"

	sqlite "modernc.org/sqlite"
)

// Chinese, Japanese and Korean put no spaces between words, and unicode61 reads
// every letter as a word character — so a whole Japanese sentence arrived at the
// index as one token, and a search for any word in it found nothing at all.
//
// There is no tokenizer to fix this with. FTS5 ships unicode61, ascii, porter
// and trigram, none of which segments CJK: trigram matches nothing shorter than
// three characters, where most Chinese and Japanese words are two, and its
// LIKE path returns no bm25 score to rank by. Registering a tokenizer of our own
// needs the FTS5 C API, and this driver is pure Go.
//
// So the text is segmented before it reaches the index. A run of CJK becomes its
// overlapping character pairs, which is what several full-text engines do for
// these scripts: it needs no dictionary, it treats all three languages the same,
// and there are no word boundaries to get wrong, only pairs that either occur or
// do not.

// segmentFuncName is what the triggers call. It has to be a function inside
// SQLite, not a step in Go, because the triggers are what keep the index and the
// messages table in step — and every path that writes a message would otherwise
// have to remember to segment, with a forgotten one leaving the index quietly
// disagreeing with the table it answers for.
const segmentFuncName = "kaiju_segment"

// segmentFuncErr is why the function is not there, if it is not. Registered in
// init so it exists before any connection is opened, and reported by the
// migration rather than swallowed: without it every insert into messages fails.
var segmentFuncErr error

func init() {
	segmentFuncErr = sqlite.RegisterDeterministicScalarFunction(segmentFuncName, 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			s, _ := args[0].(string)
			return segmentForIndex(s), nil
		})
}

// unspaced reports whether a character is written in a script that puts no
// spaces between words.
func unspaced(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// hasUnspaced reports whether any of the text is written in such a script.
func hasUnspaced(s string) bool {
	return strings.IndexFunc(s, unspaced) >= 0
}

// segmentForIndex rewrites runs of CJK as their overlapping character pairs and
// leaves everything else exactly as it was, so English keeps whole words and the
// stemming that goes with them.
func segmentForIndex(s string) string {
	return walkRuns(s, func(run []rune) string {
		if len(run) == 1 {
			return string(run)
		}
		return strings.Join(pairs(run), " ")
	})
}

// segmentForQuery rewrites the CJK in a query the way the index was written, and
// leaves the rest for the index to read as its own syntax, so AND, OR and quoted
// phrases still work on the English half of a mixed question.
func segmentForQuery(q string) string {
	return walkRuns(q, func(run []rune) string {
		// A single character has no pair of its own, so it is asked for as a
		// prefix, which finds it wherever it begins one. It is missed where it
		// only ever ends one, which is the one thing this gives up against a
		// segmenter that knows the words.
		if len(run) == 1 {
			return string(run) + "*"
		}
		// Quoted, so the pairs must appear next to each other and in that order.
		// Unquoted they would be separate terms, and a search for one word would
		// answer with every message that used those characters anywhere.
		return `"` + strings.Join(pairs(run), " ") + `"`
	})
}

// pairs is a run of characters as its overlapping pairs.
func pairs(run []rune) []string {
	out := make([]string, 0, len(run)-1)
	for i := 0; i+1 < len(run); i++ {
		out = append(out, string(run[i:i+2]))
	}
	return out
}

// walkRuns rewrites each run of CJK with rewrite and copies everything else
// through, keeping a space on either side of what it wrote.
//
// The spaces matter: unicode61 reads letters and CJK characters alike as word
// characters, so "abc犬猫" with nothing between them is a single token that no
// query will ever match.
func walkRuns(s string, rewrite func([]rune) string) string {
	buf := make([]byte, 0, len(s)*2)
	space := func() {
		if len(buf) > 0 && buf[len(buf)-1] != ' ' {
			buf = append(buf, ' ')
		}
	}
	var run []rune
	flush := func() {
		if len(run) == 0 {
			return
		}
		space()
		buf = append(buf, rewrite(run)...)
		space()
		run = run[:0]
	}
	for _, r := range s {
		if unspaced(r) {
			run = append(run, r)
			continue
		}
		flush()
		// Whitespace goes through the same one-space rule, so the separator this
		// wrote after a run and the one already in the text do not become two.
		if unicode.IsSpace(r) {
			space()
			continue
		}
		buf = utf8.AppendRune(buf, r)
	}
	flush()
	return strings.TrimSpace(string(buf))
}
