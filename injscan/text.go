package injscan

import (
	"strings"
	"unicode"
)

// The text rules below copy the Python training code (train/retrain4.py, train/retrain5b.py) exactly.
// A model only scores correctly on text cut the same way it was trained on.

const (
	windowWords  = 24 // words per window
	windowStride = 12 // words between window starts
	maxWordRunes = 64 // longer words are cut: blobs carry no meaning
)

// isSpace is Python's str.isspace: Go's unicode.IsSpace plus the four information separators.
func isSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// fields splits on whitespace as Python's str.split() does.
func fields(s string) []string {
	return strings.FieldsFunc(s, isSpace)
}

// cut keeps the first maxWordRunes characters of a word.
func cut(w string) string {
	n := 0
	for i := range w {
		if n == maxWordRunes {
			return w[:i]
		}
		n++
	}
	return w
}

// clean joins the words of s with single spaces, each word cut to maxWordRunes characters.
func clean(s string) string {
	f := fields(s)
	for i, w := range f {
		f[i] = cut(w)
	}
	return strings.Join(f, " ")
}

// tok puts spaces around every character that is not an ASCII letter, an ASCII digit or whitespace,
// so "monitoring." and "disable_monitoring" reach the word tables as "monitoring" and "disable".
func tok(s string) string {
	var b strings.Builder
	for _, r := range s {
		ascii := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if ascii || isSpace(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteByte(' ')
		b.WriteRune(r)
		b.WriteByte(' ')
	}
	return strings.Join(fields(b.String()), " ")
}

// windows returns the 24-word windows of text, 12 words apart. A text of 24 words or fewer is one window.
func windows(text string) []string {
	w := fields(text)
	if len(w) <= windowWords {
		return []string{text}
	}
	var out []string
	last := len(w) - windowWords + windowStride
	if last < 1 {
		last = 1
	}
	for i := 0; i < last; i += windowStride {
		j := i + windowWords
		if j > len(w) {
			j = len(w)
		}
		out = append(out, strings.Join(w[i:j], " "))
	}
	return out
}
