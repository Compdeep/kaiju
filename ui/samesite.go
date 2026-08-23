package ui

import (
	"net/http"
	"net/url"
)

// Refusing a request that a browser says came from another site.
//
// This matters only where there is no Authenticator. With one, every route is
// behind a token the caller must attach by hand, and a token kept in the page's
// own storage is not sent along with a request another site makes — so a page
// the operator happens to visit can ask for nothing it is not already entitled
// to.
//
// Without one, there is no token to be missing. Any page in the same browser
// can post to a loopback address, and a JSON body sent as text/plain is a
// "simple request", which means the browser sends it without asking permission
// first. The reply is unreadable to that page, and it does not need to be read:
// the request already started an agent run. Binding to loopback does not help,
// because the browser making the request is on the machine.
//
// So the rule below is what makes a mount with no sign-in defensible: the
// transport is the boundary, and this keeps a browser from carrying somebody
// else's intent across it.

/*
 * sameSiteOnly refuses a request a browser marks as coming from another site.
 * desc: Modern browsers state the provenance of every request in
 *       Sec-Fetch-Site. A caller that is not a browser sends neither that
 *       header nor Origin and is allowed through, which is correct: a script or
 *       a command-line client on the machine is the operator, and the transport
 *       is what decides who reaches it.
 *
 *       Applied only where there is no Authenticator. Applying it always would
 *       break a legitimate caller that is a browser on another origin holding a
 *       token, which is a supported way to use the API.
 * param: h - the handler to protect.
 * return: a handler that refuses cross-site requests.
 */
func sameSiteOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Sec-Fetch-Site") {
		case "same-origin", "none":
			// The page itself, or an address typed into the bar.
		case "":
			// Not a browser, or one too old to say. Fall back to Origin, which
			// a browser attaches to exactly the requests this is about.
			if o := r.Header.Get("Origin"); o != "" && !originIsHost(o, r.Host) {
				refuseCrossSite(w)
				return
			}
		default:
			// "cross-site", and "same-site" too.
			//
			// Refusing "same-site" is not caution: measured with headless
			// Chrome on 2026-08-19, a page on 127.0.0.1:18888 posting to
			// 127.0.0.1:18081 is reported as same-site, because a port is not
			// part of what a browser counts as a site. So any other program
			// listening on any other port of this machine is "same-site" and is
			// still somebody else.
			refuseCrossSite(w)
			return
		}
		h.ServeHTTP(w, r)
	})
}

/*
 * originIsHost reports whether an Origin header names the host that was asked.
 * desc: Compares hosts, not schemes: this mount is reached over plain HTTP on a
 *       loopback address, so there is no scheme to compare against. An Origin
 *       that will not parse is not this host.
 * param: origin - the Origin header.
 * param: host - r.Host, the authority the request was addressed to.
 * return: true when they are the same host.
 */
func originIsHost(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == host
}

func refuseCrossSite(w http.ResponseWriter) {
	http.Error(w, `{"error":"cross-site request refused"}`, http.StatusForbidden)
}
