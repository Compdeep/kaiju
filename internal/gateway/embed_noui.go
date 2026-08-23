//go:build noui

package gateway

import "net/http"

// The build with no interface in it.
//
// Selected by `-tags noui`. The tag removes the go:embed in embed.go, and with
// it both the megabyte and a half of built assets and the requirement that npm
// has run in this checkout — a build carrying neither can be made from a fresh
// clone, which the ordinary one cannot.
//
// WHAT THE TAG DOES NOT TURN OFF, decided rather than overlooked: the
// documentation at /docs/, which is a second embed in the docs package. Its
// files are committed, so they need no npm and cost nothing at build time, and
// what they describe — the API, the gate, the graph — is what a build with no
// interface still serves. A caller reading them is the case that build is for.
// If /docs/ is ever to go as well, it is the same decision made again about a
// different embed, not a second meaning attached to this tag.

/*
 * WebUIHandler returns the stand-in for an interface that was not built.
 * desc: Root says so; every other path is a plain 404, because this handler is
 *       mounted last and everything reaching it is a request for a static file
 *       that is not in this binary. Answering those with an explanation would
 *       put a paragraph about the web interface in front of a caller who
 *       mistyped an API path.
 * return: the handler, which never serves a file.
 */
func WebUIHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("This kaiju was built with -tags noui and carries no web interface.\n" +
			"The API is served as usual; /docs/ describes it.\n"))
	})
}
