package ui

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Changing what a machine's agent may do, from the machine's own page.
//
// A tool is in one of three states. `off` means nobody may call it. `local`
// means this machine may call it while doing its own work — someone typing in
// the chat, or the machine looking into something it noticed itself.
// `everywhere` means that, and additionally that another machine may reach
// across and make this one run it.
//
// Only the first two may be changed here, and a tool already at `everywhere`
// may not be changed here at all. Letting another machine reach in is a
// decision about the whole installation rather than about this one machine: a
// page on the machine being reached into must not be able to grant that, and
// must not be able to withdraw it either, or whoever sits at that machine can
// stop the rest of the installation doing its job.
//
// The state is held in the registry and takes effect on the next call. Keeping
// it after a restart is the application's business, which is why this is
// registered only when the application supplies somewhere to keep it — see
// Options.SetToolState.

// toolStateRequest is what the panel sends.
type toolStateRequest struct {
	Reach string `json:"reach"`
}

/*
 * toolStateHandler answers a request to change one tool's state.
 * desc: PATCH /api/v1/tools/{name} with {"reach":"off"} or {"reach":"local"}.
 *
 *       The registry is changed first and the application is told afterwards.
 *       If the application cannot keep the change, the answer says so rather
 *       than pretending: the change is real until this process restarts, and an
 *       operator who is told nothing would find it undone later with no
 *       explanation.
 * param: ag - the agent whose registry holds the states.
 * param: persist - where the application keeps them.
 * return: the handler.
 */
func toolStateHandler(ag *agent.Agent, persist func(name, state string) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.Header().Set("Allow", http.MethodPatch)
			writeToolError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		name := strings.TrimPrefix(r.URL.Path, "/api/v1/tools/")
		if name == "" || strings.Contains(name, "/") {
			writeToolError(w, http.StatusBadRequest, "the path must name exactly one tool")
			return
		}

		var req toolStateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeToolError(w, http.StatusBadRequest, "the body must be an object with a reach field")
			return
		}

		reach, ok := toolapi.ParseReach(req.Reach)
		if !ok {
			writeToolError(w, http.StatusBadRequest, "reach must be off or local")
			return
		}
		if reach == toolapi.ReachEverywhere {
			writeToolError(w, http.StatusForbidden,
				"a tool cannot be granted reach beyond this machine from this machine's own page")
			return
		}

		// What it is now decides whether it may be changed at all.
		current := ag.Registry().ReachOf(name)
		if current == toolapi.ReachEverywhere {
			writeToolError(w, http.StatusForbidden,
				"this tool is reachable from elsewhere in the installation, and that is not withdrawn from here")
			return
		}

		if err := ag.SetToolReach(name, reach); err != nil {
			writeToolError(w, http.StatusNotFound, "no such tool")
			return
		}

		kept := true
		if err := persist(name, reach.String()); err != nil {
			// The tool is already in its new state; only the record of it
			// failed. Say so rather than reporting plain success.
			log.Printf("[ui] tool %q set to %s but not kept: %v", name, reach, err)
			kept = false
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":  name,
			"reach": reach.String(),
			"kept":  kept,
		})
	})
}

func writeToolError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
