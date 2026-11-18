package llm

import (
	"net/http"
	"sync"
)

var (
	transportMu   sync.RWMutex
	transportBase http.RoundTripper
)

/*
 * SetDefaultTransport sets the transport every client made after it sends
 *       through.
 * desc: Package-scoped for the reason SetCallObserver is: clients are made in
 *       many places, some at runtime, and a hook attached per client is missed
 *       by whichever site forgot it. An application installs it once at
 *       startup, before making any client, and every client after that uses
 *       it.
 *
 *       Use it to add headers an endpoint wants, or to change where a request
 *       goes, for all of an application's model calls. A transport that wants
 *       to touch only some endpoints passes the others to its base unchanged.
 *       A client given its own transport with Transport keeps that one.
 * param: rt - the transport; nil restores the standard library's default.
 */
func SetDefaultTransport(rt http.RoundTripper) {
	transportMu.Lock()
	transportBase = rt
	transportMu.Unlock()
}

// defaultTransport is what a new client sends through: the installed one, or
// nil, which the standard library reads as http.DefaultTransport.
func defaultTransport() http.RoundTripper {
	transportMu.RLock()
	defer transportMu.RUnlock()
	return transportBase
}
