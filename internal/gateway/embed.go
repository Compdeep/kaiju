package gateway

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:web
var webFS embed.FS

// WebUIHandler returns an http.Handler serving the embedded web UI.
func WebUIHandler() http.Handler {
	sub, _ := fs.Sub(webFS, "web")
	return http.FileServer(http.FS(sub))
}
