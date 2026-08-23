package gateway

import "net/http"

// The response headers every page and every route carries.
//
// None of these replace the sanitising done where a model's reply is turned
// into HTML. They are the second answer: if a construct ever gets past that,
// the policy below is what stops the browser acting on it.

/*
 * WithSecurityHeaders sets the headers a browser needs to be told, on every
 * response.
 * desc: Four headers, each for a different way a page can be turned against the
 *       person looking at it.
 *
 *       Content-Security-Policy is the one that matters. `script-src 'self'`
 *       means the browser runs only scripts served from this origin, so an
 *       injected inline handler or a remote script does nothing even if it
 *       reaches the page. The rest of it is what the interface actually needs
 *       and no more: styles from here and inline, because the framework writes
 *       styles into the document; fonts from Google, because index.html asks
 *       for them; images from here, from data: URLs and from blob: URLs,
 *       because attachments and previews use both; frames from here, because
 *       the panel previews a built page in one.
 *
 *       X-Content-Type-Options stops the browser guessing a type other than the
 *       one declared, which is what turns an uploaded file into a script.
 *
 *       X-Frame-Options is SAMEORIGIN rather than DENY: DENY would also refuse
 *       the interface framing its own preview, which is a thing it does.
 *
 *       Referrer-Policy keeps the address of this page — which carries a
 *       session id in its path — out of requests to anywhere else.
 * param: next - the handler to wrap.
 * return: the wrapped handler.
 */
func WithSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// Measured against the running interface with a real browser rather than
// written from a template: every source below is one the page was observed to
// need, and nothing else is granted.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src 'self' https://fonts.gstatic.com data:; " +
	"img-src 'self' data: blob:; " +
	"media-src 'self' data: blob:; " +
	"connect-src 'self'; " +
	"frame-src 'self'; " +
	"frame-ancestors 'self'; " +
	"form-action 'self'; " +
	"base-uri 'self'; " +
	"object-src 'none'"
