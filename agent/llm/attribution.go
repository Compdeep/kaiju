package llm

import "sync"

// OpenRouter records HTTP-Referer and X-Title against each request and surfaces
// them publicly for attribution. When kaiju is embedded in another application,
// that application is the one making the calls and should be the one credited,
// so both headers are settable.
//
// Defaults credit kaiju itself, which is correct when kaiju runs as its own
// daemon.
const (
	defaultReferer = "https://github.com/Compdeep/kaiju"
	defaultTitle   = "Kaiju"
)

var (
	attrMu      sync.RWMutex
	attrReferer = defaultReferer
	attrTitle   = defaultTitle
)

// SetAttribution sets the HTTP-Referer and X-Title headers sent to OpenRouter.
// An empty value leaves that header at its default. Call once at startup.
//
// Package-scoped for the same reason as SetCallObserver: clients are built in
// several places, some at runtime, and a per-client setting would be missed by
// any site that forgot.
func SetAttribution(referer, title string) {
	attrMu.Lock()
	defer attrMu.Unlock()
	if referer != "" {
		attrReferer = referer
	}
	if title != "" {
		attrTitle = title
	}
}

// attribution returns the headers to send.
func attribution() (referer, title string) {
	attrMu.RLock()
	defer attrMu.RUnlock()
	return attrReferer, attrTitle
}
