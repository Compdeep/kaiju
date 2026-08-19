//go:build !noui

package gateway

import (
	"embed"
	"io/fs"
	"net/http"
)

// The interface, compiled into the binary.
//
// The directory this reads is written by `npm run build` (web/vite.config.js
// sends its output to ../internal/gateway/web) and is excluded by .gitignore,
// so it exists only in a checkout where that has been run. A go:embed of a
// missing directory is a compile error, which is why building this file at all
// is a choice: see embed_noui.go for the other one.

//go:embed all:web
var webFS embed.FS

// WebUIHandler returns an http.Handler serving the embedded web UI.
func WebUIHandler() http.Handler {
	sub, _ := fs.Sub(webFS, "web")
	return http.FileServer(http.FS(sub))
}
