package injscan

import (
	"encoding/base64"
	"encoding/hex"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Hidden text is decoded and scored beside the original: decode returns the text with terminal colour
// codes removed, followed by every readable decoding found (HTML entities, URL encoding, \u and \x
// escapes, hex, base64). It copies decode() in train/retrain4.py, in the same order.

var (
	reANSI   = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	reB64    = regexp.MustCompile(`[A-Za-z0-9+/_-]{16,}={0,2}`)
	reHex    = regexp.MustCompile(`(?:\\x)?(?:[0-9a-fA-F]{2}(?:\\x)?){8,}`)
	reUEsc   = regexp.MustCompile(`(?:\\u[0-9a-fA-F]{4}|\\x[0-9a-fA-F]{2}){4,}`)
	rePctHex = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)
)

func decode(t string) string {
	t = reANSI.ReplaceAllString(t, "")
	var extra []string
	if strings.Contains(t, "&#") || strings.Contains(t, "&amp;") || strings.Contains(t, "&lt;") {
		if u := html.UnescapeString(t); u != t {
			extra = append(extra, u)
		}
	}
	if rePctHex.MatchString(t) {
		if u := unquotePlus(t); u != t {
			extra = append(extra, u)
		}
	}
	for _, m := range reUEsc.FindAllString(t, -1) {
		extra = append(extra, unescapeU(m))
	}
	for _, m := range reHex.FindAllString(t, -1) {
		if b, err := hex.DecodeString(strings.ReplaceAll(m, `\x`, "")); err == nil {
			if s, ok := readable(b); ok {
				extra = append(extra, s)
			}
		}
	}
	for _, m := range reB64.FindAllString(t, -1) {
		g := strings.TrimRight(strings.NewReplacer("-", "+", "_", "/").Replace(m), "=")
		if b, err := base64.RawStdEncoding.DecodeString(g); err == nil {
			if s, ok := readable(b); ok {
				extra = append(extra, s)
			}
		}
	}
	if len(extra) == 0 {
		return t
	}
	return t + " " + strings.Join(extra, " ")
}

// readable accepts bytes that decode (UTF-8 first, then UTF-16 little-endian) to at least six characters,
// more than 95% printable, with a space inside.
func readable(b []byte) (string, bool) {
	if utf8.Valid(b) {
		if s := string(b); looksReadable(s) {
			return s, true
		}
	}
	if s, ok := utf16le(b); ok && looksReadable(s) {
		return s, true
	}
	return "", false
}

func looksReadable(s string) bool {
	n, p := 0, 0
	for _, r := range s {
		n++
		if unicode.IsPrint(r) {
			p++
		}
	}
	return n >= 6 && float64(p)/float64(n) > 0.95 && strings.Contains(strings.TrimFunc(s, isSpace), " ")
}

// utf16le decodes strictly, as Python's bytes.decode("utf-16-le") does: odd length or a lone
// surrogate fails.
func utf16le(b []byte) (string, bool) {
	if len(b)%2 != 0 {
		return "", false
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	for i := 0; i < len(u); i++ {
		switch {
		case u[i] >= 0xD800 && u[i] < 0xDC00:
			if i+1 >= len(u) || u[i+1] < 0xDC00 || u[i+1] > 0xDFFF {
				return "", false
			}
			i++
		case u[i] >= 0xDC00 && u[i] <= 0xDFFF:
			return "", false
		}
	}
	return string(utf16.Decode(u)), true
}

// unquotePlus is Python's urllib.parse.unquote_plus: '+' becomes a space, each valid %XX becomes a byte,
// anything else is kept, and bytes that are not UTF-8 become U+FFFD.
func unquotePlus(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '+' {
			b = append(b, ' ')
			continue
		}
		if c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			v, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			b = append(b, byte(v))
			i += 2
			continue
		}
		b = append(b, c)
	}
	var out strings.Builder
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		out.WriteRune(r) // utf8.RuneError for an invalid byte
		b = b[n:]
	}
	return out.String()
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// unescapeU decodes a run of \uXXXX and \xXX escapes.
func unescapeU(m string) string {
	var out strings.Builder
	for i := 0; i < len(m); {
		if m[i+1] == 'u' {
			v, _ := strconv.ParseUint(m[i+2:i+6], 16, 32)
			out.WriteRune(rune(v))
			i += 6
		} else {
			v, _ := strconv.ParseUint(m[i+2:i+4], 16, 32)
			out.WriteRune(rune(v))
			i += 4
		}
	}
	return out.String()
}
