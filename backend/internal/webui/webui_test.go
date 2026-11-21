package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
