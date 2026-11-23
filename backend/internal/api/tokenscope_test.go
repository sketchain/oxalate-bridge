package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every admin operation in openapi.yaml declares the admin token scope the
// middleware enforces: read for GET / HEAD, write for the rest, none (and
// cookie-only security) for the token management endpoints.
func TestOpenAPIAdminTokenScopes(t *testing.T) {
	raw, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/\S+):$`)
	methodRe := regexp.MustCompile(`^    (get|post|put|patch|delete|head):$`)
	lines := strings.Split(string(raw), "\n")
	path, n := "", 0
	for i, ln := range lines {
		if strings.HasPrefix(ln, "components:") {
			break
		}
		if m := pathRe.FindStringSubmatch(ln); m != nil {
			path = m[1]
			continue
		}
		m := methodRe.FindStringSubmatch(ln)
		if m == nil || !strings.HasPrefix(path, "/admin/") {
			continue
		}
		n++
		var scope, security string
		for _, next := range lines[i+1 : min(i+4, len(lines))] {
			if v, ok := strings.CutPrefix(next, "      x-admin-token-scope: "); ok {
				scope = v
			}
			if v, ok := strings.CutPrefix(next, "      security: "); ok {
				security = v
			}
		}
		want := "write"
		switch {
		case strings.HasPrefix(path, "/admin/admin-tokens"):
			want = "none"
		case m[1] == "get" || m[1] == "head":
			want = "read"
		}
		if scope != want {
			t.Errorf("%s %s: x-admin-token-scope %q, want %q", m[1], path, scope, want)
		}
		if hasToken := strings.Contains(security, "adminToken"); hasToken != (want != "none") {
			t.Errorf("%s %s: security %q", m[1], path, security)
		}
	}
	if n < 50 {
		t.Fatalf("only %d admin operations found", n)
	}
}
