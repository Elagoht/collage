package cli

import (
	"net/http"
	"strings"
	"testing"
)

// Next.js: packages/next/src/server/serve-static.ts serves public/ through `send`,
// whose defaults ignore dotfiles and type a file by its extension only.
// test/e2e/file-serving pins the type ("should serve avif image with correct
// content-type"). A static preview never serves a dotfile, and never guesses
// that a file with no extension is a web page.
func TestNextjs_ServeHidesDotfilesAndNeverSniffs(t *testing.T) {
	h := serving(t, map[string]string{
		"index.html":     "<h1>home</h1>",
		".env":           "SECRET=1",
		".git/config":    "[core]",
		"uploads/avatar": "<html><script>alert(document.domain)</script></html>",
	})
	for _, target := range []string{"/.env", "/.git/config"} {
		if rec := fetch(t, h, target); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d %q, want 404", target, rec.Code, rec.Body.String())
		}
	}
	rec := fetch(t, h, "/uploads/avatar")
	if ctype := rec.Header().Get("Content-Type"); strings.HasPrefix(ctype, "text/html") {
		t.Errorf("GET /uploads/avatar Content-Type = %q, sniffed as a page", ctype)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("GET /uploads/avatar has no X-Content-Type-Options: nosniff")
	}
}
