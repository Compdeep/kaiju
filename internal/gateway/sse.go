package gateway

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/user/kaiju/internal/agent"
)

/*
 * SSEHandler streams DAG events for a running investigation.
 * desc: Returns an HTTP handler that opens an SSE connection, subscribes to DAG events from the agent, and streams them as JSON.
 * param: ag - the agent whose DAG events will be streamed
 * return: an http.HandlerFunc that writes server-sent events until the client disconnects
 */
func SSEHandler(ag *agent.Agent) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

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
