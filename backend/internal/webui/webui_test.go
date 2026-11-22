package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// Without a frontend build the placeholder is served for every path; with a
// build, unknown paths fall back to index.html.
func TestHandlerServesIndexOrPlaceholder(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/", "/keys", "/admin/accounts/x"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<html") {
			t.Fatalf("%s: %d %q", path, rec.Code, rec.Body.String()[:min(80, rec.Body.Len())])
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST %d", rec.Code)
	}
}

// The service worker and the manifest get their Content-Type and no-cache
// so that a new version is picked up on the next load; hashed assets are
// immutable.
func TestHandlerHeaders(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":           {Data: []byte("<html>app</html>")},
		"sw.js":                {Data: []byte("self.addEventListener('fetch',()=>{})")},
		"manifest.webmanifest": {Data: []byte(`{"name":"Oxalate's Bridge"}`)},
		"assets/app-abc123.js": {Data: []byte("console.log(1)")},
		"icons/icon-192.png":   {Data: []byte("png")},
	}
	h := handlerFS(fsys, true)
	cases := []struct{ path, ctype, cache string }{
		{"/sw.js", "text/javascript; charset=utf-8", "no-cache"},
		{"/manifest.webmanifest", "application/manifest+json", "no-cache"},
		{"/", "text/html; charset=utf-8", "no-cache"},
		{"/keys", "text/html; charset=utf-8", "no-cache"},
		{"/assets/app-abc123.js", "", "public, max-age=31536000, immutable"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", c.path, rec.Code)
		}
		if c.ctype != "" && rec.Header().Get("Content-Type") != c.ctype {
			t.Errorf("%s: content-type %q", c.path, rec.Header().Get("Content-Type"))
		}
		if rec.Header().Get("Cache-Control") != c.cache {
			t.Errorf("%s: cache-control %q", c.path, rec.Header().Get("Cache-Control"))
		}
	}
}
