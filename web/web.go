// Package web serves the admin UI: a React app that Vite builds into dist/,
// embedded into the binary. Build it with `npm ci && npm run build` in this
// directory; the Docker build does that in its own stage.
//
// dist/robots.txt is kept in git (a copy of public/robots.txt, which Vite
// writes back on every build) so the package compiles before the first build.
package web

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

const notBuilt = "The admin UI is not built. Run in the web/ directory:\n\n  npm ci\n  npm run build\n\nthen rebuild the proxy.\n"

// Handler serves the built UI. Hashed files under /assets/ are cached for a
// year; everything else is revalidated so a new build shows up at once.
func Handler() http.Handler {
	files, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // "dist" is a constant embedded directory
	}
	return handler(files)
}

func handler(files fs.FS) http.Handler {
	if _, err := fs.Stat(files, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, notBuilt)
		})
	}
	static := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		static.ServeHTTP(w, r)
	})
}
