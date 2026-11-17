package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// An action served at both spellings of its path let a middleware that compares
// r.URL.Path to "/admin/delete" refuse one and run the handler for the other.
// Every route has one spelling; the other redirects, with the method kept.
func TestSlash_ActionAndDocumentHaveOneSpelling(t *testing.T) {
	cases := []struct {
		name   string
		slash  Slash
		method string
		path   string
		want   string
		status int
	}{
		{"action trailing slash under never", SlashNever, http.MethodPost, "/admin/delete/", "/admin/delete", http.StatusPermanentRedirect},
		{"action canonical spelling is served", SlashNever, http.MethodPost, "/admin/delete", "", 0},
		{"action keeps its query", SlashNever, http.MethodPost, "/admin/delete/?id=1", "/admin/delete?id=1", http.StatusPermanentRedirect},
		{"document trailing slash under never", SlashNever, http.MethodGet, "/sitemap.xml/", "/sitemap.xml", http.StatusMovedPermanently},
		// A document is a file in both modes, as the links built for it are.
		{"document trailing slash under always", SlashAlways, http.MethodGet, "/sitemap.xml/", "/sitemap.xml", http.StatusMovedPermanently},
		{"document is served under always", SlashAlways, http.MethodGet, "/sitemap.xml", "", 0},
		// An action on a page's own URL is spelled as ActionURL spells it, with
		// the page beside it keeping the slash TrailingSlash gives it.
		{"page action under always", SlashAlways, http.MethodPost, "/login/", "/login", http.StatusPermanentRedirect},
		{"page action served under always", SlashAlways, http.MethodPost, "/login", "", 0},
		{"page action under never", SlashNever, http.MethodPost, "/login/", "/login", http.StatusPermanentRedirect},
		{"page itself still follows its mode", SlashAlways, http.MethodGet, "/login", "/login/", http.StatusMovedPermanently},
		// Unset, the router redirects nothing.
		{"either leaves both", SlashEither, http.MethodPost, "/admin/delete/", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := New(LocaleOptions{Default: "en"}, WithSlash(tc.slash))
			if err := rt.RegisterAction(testAction("delete", "/admin/delete", http.MethodPost)); err != nil {
				t.Fatal(err)
			}
			if err := rt.RegisterDocument(testDocument("sitemap", "/sitemap.xml")); err != nil {
				t.Fatal(err)
			}
			mustRegister(t, rt, newTestPage("login", map[string]string{"en": "/login"}))
			if err := rt.RegisterAction(testAction("login-post", "/login", http.MethodPost)); err != nil {
				t.Fatal(err)
			}
			match, err := rt.Match(httptest.NewRequest(tc.method, tc.path, nil))
			if err != nil {
				t.Fatal(err)
			}
			if match.RedirectTo != tc.want || match.RedirectStatus != tc.status {
				t.Fatalf("%s %s = redirect %q %d, want %q %d", tc.method, tc.path, match.RedirectTo, match.RedirectStatus, tc.want, tc.status)
			}
			if tc.want == "" && match.IsNotFound {
				t.Fatalf("%s %s not found", tc.method, tc.path)
			}
		})
	}
}
