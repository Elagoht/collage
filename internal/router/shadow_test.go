package router

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// A redirect is matched before every route, so one with a placeholder swallowed
// each literal route it matched: WithRedirect("/{slug}", "/blog/{slug}") sent
// GET /about.txt to /blog/about.txt and the document was never served. The
// shadow check compared identical patterns only; it now also asks whether the
// redirect would take a literal route's path, in either registration order.
func TestRedirectShadow_PlaceholderRedirectMatchingALiteralRoute(t *testing.T) {
	redirect := func(from, to string) *types.Redirect { return &types.Redirect{From: from, To: to, Status: 301} }
	cases := []struct {
		name string
		from string
		to   string
		path string
	}{
		{"dynamic", "/{slug}", "/blog/{slug}", "/about.txt"},
		{"catch-all", "/old/{rest...}", "/new/{rest}", "/old/keep.txt"},
		{"affixed", "/{name}.txt", "/t/{name}", "/about.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name+" redirect first", func(t *testing.T) {
			rt := New(LocaleOptions{Default: "en"})
			mustRegister(t, rt, newTestPageWithRedirects("moved", map[string]string{"en": "/m/ved"},
				[]*types.Redirect{redirect(tc.from, tc.to)}))
			err := rt.RegisterDocument(testDocument("keep", tc.path))
			if !errors.Is(err, ErrRedirectShadowsPage) {
				t.Fatalf("RegisterDocument = %v, want ErrRedirectShadowsPage", err)
			}
			if !strings.Contains(err.Error(), tc.from) || !strings.Contains(err.Error(), "keep") {
				t.Fatalf("error %q should name the redirect %q and the document", err, tc.from)
			}
		})
		t.Run(tc.name+" route first", func(t *testing.T) {
			rt := New(LocaleOptions{Default: "en"})
			if err := rt.RegisterDocument(testDocument("keep", tc.path)); err != nil {
				t.Fatal(err)
			}
			err := rt.Register(newTestPageWithRedirects("moved", map[string]string{"en": "/m/ved"},
				[]*types.Redirect{redirect(tc.from, tc.to)}))
			if !errors.Is(err, ErrRedirectShadowsPage) {
				t.Fatalf("Register = %v, want ErrRedirectShadowsPage", err)
			}
			if !strings.Contains(err.Error(), tc.from) || !strings.Contains(err.Error(), "keep") {
				t.Fatalf("error %q should name the redirect %q and the document", err, tc.from)
			}
		})
	}
}

// The other direction: a literal redirect source that a placeholder route
// matches. The redirect wins at request time, so the route is dead for that one
// path — a hole the registration order should not hide.
func TestRedirectShadow_LiteralRedirectInsideAPlaceholderRoute(t *testing.T) {
	rt := New(LocaleOptions{Default: "en"})
	mustRegister(t, rt, newTestPage("post", map[string]string{"en": "/blog/{slug}"}))
	err := rt.Register(newTestPageWithRedirects("other", map[string]string{"en": "/other"},
		[]*types.Redirect{{From: "/blog/old", To: "/other", Status: http.StatusMovedPermanently}}))
	if !errors.Is(err, ErrRedirectShadowsPage) {
		t.Fatalf("Register = %v, want ErrRedirectShadowsPage", err)
	}
}

// A redirect that matches no registered route's path is untouched.
func TestRedirectShadow_DisjointRedirectsStillRegister(t *testing.T) {
	rt := New(LocaleOptions{Default: "en"})
	if err := rt.RegisterDocument(testDocument("keep", "/about.txt")); err != nil {
		t.Fatal(err)
	}
	mustRegister(t, rt, newTestPageWithRedirects("moved", map[string]string{"en": "/blog/{slug}"},
		[]*types.Redirect{{From: "/old/{slug}", To: "/blog/{slug}", Status: 301}}))
}
