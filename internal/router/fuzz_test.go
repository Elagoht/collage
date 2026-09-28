package router

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// sameSite reports whether a browser following location stays on the origin
// that sent it: the browser's reading, which drops tabs and newlines and takes a
// backslash for a slash before resolving.
func sameSite(location string) bool {
	loc := strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\n', '\r':
			return -1
		case '\\':
			return '/'
		}
		return r
	}, location)
	// A path: one slash, then anything but a second. Whatever follows — a
	// query, a fragment, bytes url.Parse would refuse — stays on this origin.
	return strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//")
}

// Whatever path a client sends, matching answers — a page, a redirect, not
// found — and never fails, panics, or redirects off the site. Across every
// locale arrangement, because each adds a redirect of its own.
func FuzzMatch(f *testing.F) {
	for _, seed := range []string{
		"/", "/blog/x", "/tr/blog/x", "/EN/blog/x", "/s%E2%84%AA/admin", "/%C5%BFk/",
		"/old/%5Cevil.com", "/old/%2Fevil.com", "/docs/a/b", "/files%2Fx", "/%", "/%zz",
		"/a%00b", "/\t/evil.com", "/blog/x/", "//evil.com", "/%09/x",
	} {
		f.Add(seed, false, false)
	}
	f.Fuzz(func(t *testing.T, target string, prefixDefault, disable bool) {
		if !strings.HasPrefix(target, "/") {
			return
		}
		u, err := url.ParseRequestURI(target)
		if err != nil {
			return
		}
		rt := New(LocaleOptions{Default: "en", Supported: []string{"en", "tr", "sk"}, PrefixDefault: prefixDefault, DisablePathLocale: disable})
		for _, page := range []*types.Page{
			{Name: "home", Paths: map[string]string{"en": "/", "tr": "/"}},
			{Name: "post", Paths: map[string]string{"en": "/blog/{slug}", "tr": "/blog/{slug}"}},
			{Name: "docs", Paths: map[string]string{"en": "/docs/{rest...}"}},
			{Name: "admin", Paths: map[string]string{"en": "/admin"},
				Redirects: []*types.Redirect{{From: "/old/{slug}", To: "/blog/{slug}"}, {From: "/legacy/{rest...}", To: "/docs/{rest}"}}},
		} {
			if err := rt.Register(page); err != nil {
				t.Fatalf("Register(%q): %v", page.Name, err)
			}
		}
		req := &http.Request{Method: http.MethodGet, URL: u, Host: "site.test", Header: http.Header{}}
		result, err := rt.Match(req)
		if err != nil {
			t.Fatalf("Match(%q) = %v, want an answer", target, err)
		}
		if result.RedirectTo != "" && !sameSite(result.RedirectTo) {
			t.Fatalf("Match(%q) redirects to %q, off the site", target, result.RedirectTo)
		}
		for name, value := range result.PathParams {
			if name != "rest" && strings.Contains(value, "/") {
				t.Fatalf("Match(%q): %s = %q holds a slash a middleware reads as a separator", target, name, value)
			}
		}
	})
}
