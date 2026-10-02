package router

import (
	"net/http"
	"net/url"
	"path"
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
		"/blog/x.md", "/blog/.md", "/blog/...md", "/blog/..md", "/old/x.html", "/old/%2F.html", "/tr/blog/x.md", "/post-1",
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
		// With path locales off nothing reaches "tr", and a path there is refused.
		inTR := func(paths map[string]string) map[string]string {
			if !disable {
				paths["tr"] = paths["en"]
			}
			return paths
		}
		for _, page := range []*types.Page{
			{Name: "home", Paths: inTR(map[string]string{"en": "/"})},
			{Name: "post", Paths: inTR(map[string]string{"en": "/blog/{slug}"})},
			{Name: "post-md", Paths: inTR(map[string]string{"en": "/blog/{slug}.md"})},
			{Name: "short", Paths: map[string]string{"en": "/post-{id}"},
				Redirects: []*types.Redirect{{From: "/old/{slug}.html", To: "/blog/{slug}.md"}}},
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
			if name != "rest" && value == "" {
				t.Fatalf("Match(%q): %s is empty", target, name)
			}
			// A path holding a dot segment is redirected to its clean form before
			// it reaches the router; of the rest, none may hand a handler one —
			// "/blog/...md" against "{slug}.md" once captured "..".
			if name != "rest" && (value == "." || value == "..") && path.Clean(u.Path) == strings.TrimSuffix(u.Path, "/") {
				t.Fatalf("Match(%q): %s = %q, which a handler joining it onto a directory climbs out with", target, name, value)
			}
		}
	})
}

// Two placeholders with text around them, at one position: registration refuses
// exactly the crossing pairs, and a segment matches the more specific of those
// that match it, whichever registered first.
func FuzzAffixedPair(f *testing.F) {
	f.Add("", ".md", "", ".min.md", "app.min.md", false)
	f.Add("a", "", "", "b", "ab", true)
	f.Add("post-", "", "", "", "post-1", false)
	f.Fuzz(func(t *testing.T, p1, s1, p2, s2, seg string, swap bool) {
		for _, text := range []string{p1, s1, p2, s2, seg} {
			if strings.ContainsAny(text, "{}/%?#") || len(text) > 8 {
				return
			}
		}
		// A segment spelling the locale is the locale's front page, whatever
		// else is registered.
		if seg == "" || seg == "." || seg == ".." || strings.EqualFold(seg, "en") {
			return
		}
		a := patternSegment{kind: segmentDynamic, text: "a", prefix: p1, suffix: s1}
		b := patternSegment{kind: segmentDynamic, text: "b", prefix: p2, suffix: s2}
		if a.prefix == b.prefix && a.suffix == b.suffix {
			return
		}
		first, second := a, b
		if swap {
			first, second = b, a
		}
		rt := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
		register := func(s patternSegment) error {
			return rt.Register(&types.Page{Name: s.text, Paths: map[string]string{"en": "/" + s.literal()}})
		}
		if err := register(first); err != nil {
			t.Fatalf("Register(%q) = %v", first.literal(), err)
		}
		crossing := overlaps(a, b) && !within(a, b) && !within(b, a)
		err := register(second)
		if crossing != (err != nil) {
			t.Fatalf("%q and %q: crossing=%v, Register = %v", first.literal(), second.literal(), crossing, err)
		}
		if crossing {
			return
		}
		ma := (&affixEdge{segment: a}).capture
		mb := (&affixEdge{segment: b}).capture
		_, inA := ma(seg)
		_, inB := mb(seg)
		want := ""
		switch {
		case inA && inB && within(a, b):
			want = "a"
		case inA && inB:
			want = "b"
		case inA:
			want = "a"
		case inB:
			want = "b"
		}
		u := &url.URL{Path: "/" + seg}
		result, err := rt.Match(&http.Request{Method: http.MethodGet, URL: u, Host: "site.test", Header: http.Header{}})
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if result.Page != nil {
			got = result.Page.Name
		}
		if got != want {
			t.Fatalf("%q and %q, segment %q: matched %q, want %q", a.literal(), b.literal(), seg, got, want)
		}
	})
}
