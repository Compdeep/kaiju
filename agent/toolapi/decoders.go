package toolapi

import (
	"context"
	"strings"
	"sync"
)

// Reader-enrichment seams. Core web_fetch does a plain HTTP GET + readability;
// optional, build-tag-gated plugins teach it to read MORE without pulling heavy
// dependencies into the default binary:
//
//   - a BinaryDecoder turns a typed body (e.g. application/pdf) into text — the
//     pdf plugin registers one so web_fetch can read a PDF a search turned up.
//   - a ReaderFallback re-reads a URL that yielded no extractable content (a
//     JS-rendered SPA, a login/consent wall) through a heavier path — the
//     webreader plugin registers one (headless render / extraction service).
//
// Registration happens at plugin activation (startup); lookups at request time.
// With no plugin compiled in, every lookup is a miss and web_fetch behaves
// exactly as it always has.
var (
	decodersMu     sync.RWMutex
	binaryDecoders = map[string]func([]byte) (string, error){}
	readerFallback func(ctx context.Context, rawURL string) (string, error)
)

// normalizeMime lowercases a Content-Type and drops any "; charset=…" suffix.
func normalizeMime(contentType string) string {
	m := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	return m
}

// RegisterBinaryDecoder maps a MIME type to a text decoder. Last write wins.
func RegisterBinaryDecoder(mime string, fn func([]byte) (string, error)) {
	decodersMu.Lock()
	defer decodersMu.Unlock()
	binaryDecoders[normalizeMime(mime)] = fn
}

// HasBinaryDecoder reports whether a decoder is registered for a Content-Type.
// web_fetch calls this BEFORE reading the body so it can lift its read cap for a
// document it is about to parse — a PDF is MBs and the HTML cap would corrupt it.
func HasBinaryDecoder(contentType string) bool {
	m := normalizeMime(contentType)
	if m == "" {
		return false
	}
	decodersMu.RLock()
	defer decodersMu.RUnlock()
	_, ok := binaryDecoders[m]
	return ok
}

// DecodeBinary runs the decoder registered for contentType, if any. found=false
// means no decoder applies and the caller keeps its normal (HTML) path.
func DecodeBinary(contentType string, data []byte) (text string, found bool, err error) {
	m := normalizeMime(contentType)
	if m == "" {
		return "", false, nil
	}
	decodersMu.RLock()
	fn := binaryDecoders[m]
	decodersMu.RUnlock()
	if fn == nil {
		return "", false, nil
	}
	t, e := fn(data)
	return t, true, e
}

// RegisterReaderFallback installs the heavy re-read path (render + extract). One
// at a time; last write wins.
func RegisterReaderFallback(fn func(ctx context.Context, rawURL string) (string, error)) {
	decodersMu.Lock()
	defer decodersMu.Unlock()
	readerFallback = fn
}

// ReaderFallback runs the registered fallback, if any. ok=false means no plugin
// provided one, so core returns its normal "no extractable content" signal.
func ReaderFallback(ctx context.Context, rawURL string) (text string, ok bool, err error) {
	decodersMu.RLock()
	fn := readerFallback
	decodersMu.RUnlock()
	if fn == nil {
		return "", false, nil
	}
	t, e := fn(ctx, rawURL)
	return t, true, e
}
