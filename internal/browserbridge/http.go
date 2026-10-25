package browserbridge

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Compdeep/kaiju/internal/db"
)

/*
 * Routes mounts the bridge's endpoints on mux.
 * desc: The websocket is open, because the extension proves itself in its
 *       first frame rather than with a session token. The three that act for
 *       a person — status, pair, revoke — go through protect, the same check
 *       every other route behind the sign-in uses, and read who that person is
 *       with userOf.
 * param: mux - where to mount.
 * param: protect - the sign-in check; the identity wrapper on a node without one.
 * param: userOf - who the request is from; empty on a node without sign-in.
 */
func (b *Bridge) Routes(mux *http.ServeMux, protect func(http.Handler) http.Handler, userOf func(*http.Request) string) {
	mux.Handle("/api/v1/browser/ws", b.Handler())
	mux.Handle("GET /api/v1/browser/status", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := b.Status(userOf(r))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"browsers": list})
	})))
	mux.Handle("POST /api/v1/browser/pair", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, expires, err := b.Pair(userOf(r))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"code": code, "expires_at": expires.UTC().Format(time.RFC3339)})
	})))
	mux.Handle("DELETE /api/v1/browser/devices/{id}", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := b.Revoke(userOf(r), r.PathValue("id"))
		switch {
		case errors.Is(err, db.ErrBrowserDeviceUnknown):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
		}
	})))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
