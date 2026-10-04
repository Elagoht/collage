package collage_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

func TestSafeRedirect(t *testing.T) {
	cases := []struct{ next, fallback, want string }{
		{"/panel", "/", "/panel"},
		{"/panel?tab=2#x", "/", "/panel?tab=2#x"},
		{"/çay", "/", "/çay"},
		{"//evil.com", "/home", "/home"},
		{`/\evil.com`, "/home", "/home"},
		{"https://evil.com", "/home", "/home"},
		{"javascript:alert(1)", "/home", "/home"},
		{"/a\nb", "/home", "/home"},
		{"/a\x7fb", "/home", "/home"},
		{"", "/home", "/home"},
		{"panel", "/home", "/home"},
		{"//evil.com", "https://evil.com", "/"},
		{"", "", "/"},
		// http.Redirect cleans a rooted path; what it cleans into must be safe too.
		{`/./\evil.com`, "/home", "/home"},
		{`/a/../\evil.com`, "/home", "/home"},
		{`/a\b`, "/home", "/home"},
		{`/search?q=a\b`, "/home", "/home"},
		{"//evil.com", `/./\evil.com`, "/"},
		// Cleaned to /evil.com, a path on this site: kept.
		{"/.//evil.com", "/home", "/.//evil.com"},
		{"/a/..//evil.com", "/home", "/a/..//evil.com"},
		// Escaped, so never read as a separator: kept.
		{"/%2F%2Fevil.com", "/home", "/%2F%2Fevil.com"},
		{"/%5Cevil.com", "/home", "/%5Cevil.com"},
		{"/ /x", "/home", "/ /x"},
	}
	for _, c := range cases {
		if got := collage.SafeRedirect(c.next, c.fallback); got != c.want {
			t.Errorf("SafeRedirect(%q, %q) = %q, want %q", c.next, c.fallback, got, c.want)
		}
	}
}

// What SafeRedirect lets through, http.Redirect must not turn into a
// protocol-relative Location.
func TestSafeRedirect_ThroughHTTPRedirect(t *testing.T) {
	for _, next := range []string{
		"/panel", `/./\evil.com`, `/a/../\evil.com`, "/.//evil.com", "/a/..//evil.com",
		"/%2F%2Fevil.com", "/%5Cevil.com", "/ /x", "/./", "/a/../", "/..//evil.com",
		"/../../\\evil.com", "/.?x=//evil.com", "//evil.com", `/\evil.com`,
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/login", nil)
		http.Redirect(w, r, collage.SafeRedirect(next, "/"), http.StatusSeeOther)
		loc := w.Header().Get("Location")
		if strings.HasPrefix(loc, "//") || strings.HasPrefix(loc, `/\`) || !strings.HasPrefix(loc, "/") {
			t.Errorf("next %q: Location %q leaves the site", next, loc)
		}
	}
}
