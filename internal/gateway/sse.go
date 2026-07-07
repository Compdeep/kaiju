package gateway

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/Compdeep/kaiju/internal/agent"
	"github.com/Compdeep/kaiju/internal/db"
)

/*
 * SSEHandler streams DAG events to the authenticated caller, filtered to only the
 * sessions that caller owns.
 * desc: The DAG event bus is process-global (it carries every investigation's
 *       events), so isolation is enforced HERE at the source: the handler reads
 *       the caller's principal from the JWT claims (validated ONCE by the auth
 *       middleware when the stream opens) and forwards an event only if its
 *       SessionID belongs to that principal. Ownership is memoized per connection,
 *       so the steady-state per-event cost is a map lookup — no JWT check and no
 *       DB query per event. Events with no session, or sessions owned by other
 *       principals, are dropped, making per-principal isolation a structural
 *       guarantee rather than something a downstream consumer must route.
 *
 *       Mount behind WithJWTAuth / WithJWTAuthOrQuery so claims are present
 *       (browsers can't set headers on an EventSource, so the query-token variant
 *       is the usual choice).
 * param: ag - the agent whose DAG events are streamed
 * param: database - session store, used to check per-event ownership
 * return: an http.HandlerFunc that streams server-sent events until the client disconnects
 */
func SSEHandler(ag *agent.Agent, database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
			return
		}
		principal := claims.Username

		// Per-connection ownership memo. A session's owner is set at creation and
		// never changes, so caching both hits and misses is safe: an event's
		// session belongs to this principal, or it never will. The DB is touched
		// at most once per distinct session seen on this connection.
		owned := map[string]bool{}
		ownsSession := func(sessionID string) bool {
			if sessionID == "" {
				return false // unattributable — never forward on a per-principal stream
			}
			if v, seen := owned[sessionID]; seen {
				return v
			}
			_, err := database.GetSessionForUser(sessionID, principal)
			v := err == nil
			owned[sessionID] = v
			return v
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		// Flush headers + an initial keepalive immediately so the client sees a
		// prompt 200 and knows the stream is live before the first event.
		fmt.Fprint(w, ": connected\n\n")
		flusher.Flush()

		ch, unsub := ag.SubscribeDAG()
		defer unsub()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if !ownsSession(ev.SessionID) {
					continue // not this caller's session — drop (isolation)
				}
				data, err := json.Marshal(ev)
				if err != nil {
					log.Printf("[sse] marshal: %v", err)
					continue
				}
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}
