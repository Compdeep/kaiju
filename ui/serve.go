package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ConfigPath is where the page asks for its configuration.
//
// Exported because two places need the same string and neither owns it: the
// server mounting the handler, and anything checking that it was mounted.
const ConfigPath = "/api/v1/ui"

/*
 * ConfigHandler serves the configuration a page reads before it mounts.
 * desc: Answers GET with the configuration as JSON and nothing else. It is
 *       deliberately outside authentication: the sign-in page carries the brand
 *       too, and it is drawn before anyone has a token. Nothing secret can be
 *       added to it later without that becoming untrue, which is why Config
 *       holds only a name, a set of colours and a set of switches.
 *
 *       Read-only, and not part of any configuration a request can change. A
 *       theme arriving over the network would be a stylesheet arriving over the
 *       network.
 *
 *       The body is built once. The configuration cannot change while the
 *       process runs; an application that wants a different one builds a new
 *       handler.
 * param: cfg - what to serve.
 * return: the handler, or an error if the configuration is not usable.
 */
func ConfigHandler(cfg Config) (http.Handler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("ui: encode config: %w", err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// The page asks for this once per load and must not be given a stale
		// answer after an operator has changed it and restarted.
		w.Header().Set("Cache-Control", "no-store")
		w.Write(body)
	}), nil
}
