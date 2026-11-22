// Package webui serves the embedded panel frontend. The frontend build
// (web/, `pnpm build`) writes its output to ./dist, which is embedded at
// compile time. The repository only holds dist/.gitkeep; without a build the
// placeholder page in ./placeholder is served instead, so the Go code always
// compiles.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

//go:embed placeholder/index.html
var placeholder []byte

// Built reports whether a frontend build is embedded.
func Built() bool {
	_, err := fs.Stat(dist, "dist/index.html")
	return err == nil
}

// Handler serves static files and falls back to index.html for SPA routes.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return handlerFS(sub, Built())
}

// noCache are files that must be revalidated on every load: the app shell,
// the service worker (so a new version is picked up at once) and the web
// app manifest.
var noCache = map[string]string{
	"index.html":           "text/html; charset=utf-8",
	"sw.js":                "text/javascript; charset=utf-8",
	"manifest.webmanifest": "application/manifest+json",
	"offline.html":         "text/html; charset=utf-8",
}

func handlerFS(sub fs.FS, built bool) http.Handler {
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !built {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(placeholder)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if st, err := fs.Stat(sub, p); err != nil || st.IsDir() {
			// SPA route: serve index.html without caching.
			w.Header().Set("Cache-Control", "no-cache")
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		} else if ct, ok := noCache[p]; ok {
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Cache-Control", "no-cache")
			if p == "sw.js" {
				w.Header().Set("Service-Worker-Allowed", "/")
			}
		} else if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		if p == "index.html" {
			// FileServer redirects /index.html to /; serve it directly.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}
