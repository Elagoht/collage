package router

import (
	"errors"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// A redirect whose destination its own source matches never arrives: the browser
// follows it until it gives up. WithRedirect("/docs", "/docs/") is the quiet one,
// since a source matches with or without its trailing slash.
func TestRedirect_RefusesALoop(t *testing.T) {
	cases := []struct{ from, to string }{
		{"/loop", "/loop"},
		{"/docs", "/docs/"},
		{"/docs/", "/docs"},
		{"/blog/{slug}", "/blog/{slug}"},
		{"/{x}", "/{x}"},
		{"/{x}", "/a"},
		{"/docs/{rest...}", "/docs/v2/{rest}"},
		{"/loop", "/loop?from=old"},
		{"/loop", "/loop#top"},
		// The locale prefix is stripped before a redirect is matched, so a
		// destination under it meets the same source.
		{"/about", "/tr/about"},
	}
	for _, tc := range cases {
		rt := New(LocaleOptions{Default: "en", Supported: []string{"en", "tr"}})
		page := newTestPageWithRedirects("p", map[string]string{"en": "/p/q/r"}, []*types.Redirect{{From: tc.from, To: tc.to, Status: 302}})
		if err := rt.Register(page); !errors.Is(err, ErrRedirectLoop) {
			t.Errorf("redirect %q -> %q = %v, want ErrRedirectLoop", tc.from, tc.to, err)
		}
	}
}

func TestRedirect_NonLoopsStillRegister(t *testing.T) {
	cases := []struct{ from, to string }{
		{"/old-blog/{slug}", "/blog/{slug}"},
		{"/old", "/new"},
		{"/blog/{slug}", "/posts/{slug}"},
		{"/{x}", "/new/{x}"},
		{"/old/{rest...}", "/new/{rest}"},
		{"/old", "/new?old=1"},
	}
	for _, tc := range cases {
		rt := New(LocaleOptions{Default: "en", Supported: []string{"en", "tr"}})
		page := newTestPageWithRedirects("p", map[string]string{"en": "/p/q/r"}, []*types.Redirect{{From: tc.from, To: tc.to, Status: 302}})
		if err := rt.Register(page); err != nil {
			t.Errorf("redirect %q -> %q = %v, want it registered", tc.from, tc.to, err)
		}
	}
}

// One name for two placeholders has no single answer when the link is built from
// a parameter map, so app.URL failed for the route every time.
func TestParsePattern_RepeatedPlaceholderNameIsInvalid(t *testing.T) {
	for _, pattern := range []string{"/dup/{id}/x/{id}", "/{id}/{id}", "/{id}.md/{id}", "/a/{id}/{id...}"} {
		if _, err := parsePattern(pattern); !errors.Is(err, ErrInvalidPattern) {
			t.Errorf("parsePattern(%q) = %v, want ErrInvalidPattern", pattern, err)
		}
	}
	if _, err := parsePattern("/a/{id}/b/{other}"); err != nil {
		t.Errorf("distinct names: %v", err)
	}
}
