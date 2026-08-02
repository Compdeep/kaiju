// Package docs embeds kaiju's documentation and serves it under /docs/. The
// architecture overview (architecture.html) is a hand-built page; the rest are
// markdown reference docs. Served WITHOUT auth — documentation is public, the
// same way the sibling web UI is. The embed lives here, inside docs/, because
// go:embed can only reach files under the embedding source file's own directory.
package docs

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed architecture.html *.md
var files embed.FS

// Handler serves the embedded docs at /docs/. Bare "/docs/" and an extensionless
// path resolve to the matching .html (so /docs/architecture works, matching the
// clean URL style); anything else is served as the named file (e.g. the .md
// reference docs at /docs/graph.md).
func Handler() http.Handler {
	fileServer := http.FileServer(http.FS(files))
	return http.StripPrefix("/docs/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" || p == "." {
			p = "architecture.html" // /docs/ → the architecture overview
		}
		if path.Ext(p) == "" {
			if _, err := fs.Stat(files, p+".html"); err == nil {
				p += ".html"
			}
		}
		// Markdown docs render to a styled HTML page, unless ?raw is asked for
		// (then the raw markdown is served as before, via the file server).
		if path.Ext(p) == ".md" && !r.URL.Query().Has("raw") {
			data, err := files.ReadFile(p)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(RenderMarkdown(data, docTitle(p, data)))
			return
		}
		r.URL.Path = "/" + p
		fileServer.ServeHTTP(w, r)
	}))
}
