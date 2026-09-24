package router

import (
	"errors"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

func TestBuildPath(t *testing.T) {
	for _, c := range []struct {
		pattern string
		params  map[string]string
		want    string
	}{
		{"/", nil, "/"},
		{"/about", nil, "/about"},
		{"/blog/{slug}", map[string]string{"slug": "hello"}, "/blog/hello"},
		{"/blog/{slug}", map[string]string{"slug": "a/b c"}, "/blog/a%2Fb%20c"},
		{"/docs/{rest...}", map[string]string{"rest": "guide/intro"}, "/docs/guide/intro"},
		{"/u/{id}/posts/{post}", map[string]string{"id": "7", "post": "9"}, "/u/7/posts/9"},
	} {
		got, err := BuildPath(c.pattern, c.params)
		if err != nil || got != c.want {
			t.Errorf("BuildPath(%q, %v) = %q, %v; want %q", c.pattern, c.params, got, err, c.want)
		}
	}
}

// A link that cannot be built exactly is an error, not a guess.
func TestBuildPath_Refusals(t *testing.T) {
	for _, c := range []struct {
		pattern string
		params  map[string]string
	}{
		{"/blog/{slug}", nil},
		{"/blog/{slug}", map[string]string{"slug": ""}},
		{"/blog/{slug}", map[string]string{"slug": "x", "extra": "y"}},
		{"/about", map[string]string{"slug": "x"}},
		{"/blog/{slug}", map[string]string{"slug": ".."}},
		{"/docs/{rest...}", map[string]string{"rest": "a/../b"}},
		{"/docs/{rest...}", map[string]string{"rest": "a//b"}},
	} {
		if _, err := BuildPath(c.pattern, c.params); !errors.Is(err, types.ErrRouteParams) {
			t.Errorf("BuildPath(%q, %v) = %v, want ErrRouteParams", c.pattern, c.params, err)
		}
	}
}

// What BuildPath writes, the router reads back as the same values.
func TestBuildPath_RoundTripsThroughMatch(t *testing.T) {
	rt := New(LocaleOptions{Default: "en"})
	page := &types.Page{Name: "post", Paths: map[string]string{"en": "/blog/{slug}"}}
	if err := rt.Register(page); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"hello", "a/b", "çay ?#%", "..."} {
		path, err := BuildPath("/blog/{slug}", map[string]string{"slug": slug})
		if err != nil {
			t.Fatalf("BuildPath(%q) = %v", slug, err)
		}
		match := matchPath(t, rt, path)
		if match.Page == nil || match.PathParams["slug"] != slug {
			t.Errorf("slug %q -> %q -> %+v; want it matched back", slug, path, match)
		}
	}
}
