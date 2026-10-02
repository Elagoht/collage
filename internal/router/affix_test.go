package router

import (
	"errors"
	"net/url"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

func TestParsePattern_Affixed(t *testing.T) {
	for pattern, want := range map[string]patternSegment{
		"/{slug}.md":        {kind: segmentDynamic, text: "slug", suffix: ".md"},
		"/post-{id}":        {kind: segmentDynamic, text: "id", prefix: "post-"},
		"/v{version}.json":  {kind: segmentDynamic, text: "version", prefix: "v", suffix: ".json"},
		"/{slug}":           {kind: segmentDynamic, text: "slug"},
		"/{category}.xml/x": {kind: segmentDynamic, text: "category", suffix: ".xml"},
	} {
		if got := mustParse(t, pattern)[0]; got != want {
			t.Errorf("parsePattern(%q)[0] = %+v, want %+v", pattern, got, want)
		}
	}
	if got := normalizePattern("/blogs/{slug}.md"); got != "/blogs/{}.md" {
		t.Errorf("normalizePattern = %q", got)
	}
}

// overlaps and within are exact: checked here against every segment up to a
// length over a small alphabet, which is every way two short affixes can meet.
func TestOverlapsAndWithinAreExact(t *testing.T) {
	affixes := []string{"", "a", "b", "ab", "ba", "aa"}
	var segments []string
	var grow func(string)
	grow = func(s string) {
		if len(s) > 6 {
			return
		}
		segments = append(segments, s)
		grow(s + "a")
		grow(s + "b")
	}
	grow("")
	matches := func(seg patternSegment, s string) bool {
		_, ok := (&affixEdge{segment: seg}).capture(s)
		return ok
	}
	for _, p1 := range affixes {
		for _, s1 := range affixes {
			for _, p2 := range affixes {
				for _, s2 := range affixes {
					a := patternSegment{kind: segmentDynamic, text: "x", prefix: p1, suffix: s1}
					b := patternSegment{kind: segmentDynamic, text: "x", prefix: p2, suffix: s2}
					both, onlyA := false, false
					for _, s := range segments {
						ma, mb := matches(a, s), matches(b, s)
						both = both || (ma && mb)
						onlyA = onlyA || (ma && !mb)
					}
					if overlaps(a, b) != both {
						t.Errorf("overlaps(%s, %s) = %v, but a segment matching both exists: %v", a.literal(), b.literal(), overlaps(a, b), both)
					}
					if within(a, b) != !onlyA {
						t.Errorf("within(%s, %s) = %v, but a segment only a matches exists: %v", a.literal(), b.literal(), within(a, b), onlyA)
					}
				}
			}
		}
	}
}

func affixRouter(t *testing.T, routes map[string]string) Router {
	t.Helper()
	rt := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	for name, pattern := range routes {
		if err := rt.Register(newTestPage(name, map[string]string{"en": pattern})); err != nil {
			t.Fatalf("Register(%q) = %v", pattern, err)
		}
	}
	return rt
}

// A page at /blogs/{slug} and a document at /blogs/{slug}.md stand side by side:
// the text decides, a static segment beats both, and a segment with nothing
// between the affixes is the bare placeholder's.
func TestAffixedPlaceholderBesideABareOne(t *testing.T) {
	rt := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	page := newTestPage("post", map[string]string{"en": "/blogs/{slug}"})
	index := newTestPage("index", map[string]string{"en": "/blogs/index.md"})
	doc := testDocument("post-md", "/blogs/{slug}.md")
	// The document first: the order of registration decides nothing.
	if err := rt.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*types.Page{page, index} {
		if err := rt.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	for path, want := range map[string]struct {
		route string
		slug  string
	}{
		"/blogs/hello":        {"post", "hello"},
		"/blogs/hello.md":     {"post-md", "hello"},
		"/blogs/a.b.md":       {"post-md", "a.b"},
		"/blogs/hello%2Emd":   {"post-md", "hello"},
		"/blogs/.md":          {"post", ".md"},
		"/blogs/index.md":     {"index", ""},
		"/blogs/hello.md.txt": {"post", "hello.md.txt"},
	} {
		m := matchPath(t, rt, path)
		got := ""
		switch {
		case m.Page != nil:
			got = m.Page.Name
		case m.Document != nil:
			got = m.Document.Name
		}
		if got != want.route || m.PathParams["slug"] != want.slug {
			t.Errorf("%s -> %s slug=%q, want %s slug=%q", path, got, m.PathParams["slug"], want.route, want.slug)
		}
	}
}

// Of two nested placeholders the more specific wins, whichever registered first;
// prefixes and both together work the same way.
func TestTheMoreSpecificPlaceholderWins(t *testing.T) {
	for _, order := range [][2]string{{"/f/{x}.md", "/f/{x}.min.md"}, {"/f/{x}.min.md", "/f/{x}.md"}} {
		rt := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
		for _, pattern := range order {
			if err := rt.Register(newTestPage(pattern, map[string]string{"en": pattern})); err != nil {
				t.Fatal(err)
			}
		}
		if m := matchPath(t, rt, "/f/app.min.md"); m.Page == nil || m.Page.Name != "/f/{x}.min.md" || m.PathParams["x"] != "app" {
			t.Errorf("order %v: /f/app.min.md -> %+v", order, m)
		}
		if m := matchPath(t, rt, "/f/app.md"); m.Page == nil || m.Page.Name != "/f/{x}.md" || m.PathParams["x"] != "app" {
			t.Errorf("order %v: /f/app.md -> %+v", order, m)
		}
	}
	rt := affixRouter(t, map[string]string{"post": "/post-{id}", "release": "/v{version}.json", "any": "/{page}"})
	for path, want := range map[string][2]string{
		"/post-12":     {"post", "12"},
		"/v1.2.json":   {"release", "1.2"},
		"/post-":       {"any", "post-"},
		"/vx.json.bak": {"any", "vx.json.bak"},
		"/hello":       {"any", "hello"},
	} {
		m := matchPath(t, rt, path)
		if m.Page == nil || m.Page.Name != want[0] {
			t.Errorf("%s -> %+v, want %s", path, m.Page, want[0])
			continue
		}
		for _, v := range m.PathParams {
			if v != want[1] {
				t.Errorf("%s captured %q, want %q", path, v, want[1])
			}
		}
	}
}

// Crossing placeholders — some segment matches both, neither is more specific —
// are refused in either order, as is one spelling under two names.
func TestCrossingPlaceholdersAreRefused(t *testing.T) {
	for _, pair := range [][2]string{
		{"/a{x}", "/{x}b"},
		{"/{x}b", "/a{x}"},
		{"/ab{x}", "/a{x}b"},
		{"/p/{x}.md", "/p/v{x}"},
	} {
		rt := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
		if err := rt.Register(newTestPage("one", map[string]string{"en": pair[0]})); err != nil {
			t.Fatal(err)
		}
		if err := rt.Register(newTestPage("two", map[string]string{"en": pair[1]})); !errors.Is(err, ErrOverlappingPattern) {
			t.Errorf("%s then %s: %v, want ErrOverlappingPattern", pair[0], pair[1], err)
		}
	}
	rt := affixRouter(t, map[string]string{"one": "/{slug}.md"})
	if err := rt.Register(newTestPage("two", map[string]string{"en": "/{name}.md"})); !errors.Is(err, ErrAmbiguousParameterName) {
		t.Errorf("{slug}.md then {name}.md: %v, want ErrAmbiguousParameterName", err)
	}
	rt = affixRouter(t, map[string]string{"one": "/{slug}.md"})
	if err := rt.Register(newTestPage("two", map[string]string{"en": "/{x}.md"})); !errors.Is(err, ErrAmbiguousParameterName) {
		t.Errorf("same affixes, other name: %v", err)
	}
	// Disjoint ones are fine, and so is the same one with the same name.
	affixRouter(t, map[string]string{"md": "/{slug}.md", "json": "/{slug}.json", "rss": "/{slug}.rss"})
	rt = affixRouter(t, map[string]string{"one": "/{slug}.md"})
	if err := rt.RegisterDocument(testDocument("two", "/{slug}.md/raw")); err != nil {
		t.Errorf("a longer pattern through the same edge: %v", err)
	}
}

// A match backtracks out of an affixed edge that leads nowhere, as it does out of
// a static one.
func TestAffixedEdgeBacktracks(t *testing.T) {
	rt := affixRouter(t, map[string]string{"edit": "/x/{a}.md/edit", "view": "/x/{b}/view"})
	if m := matchPath(t, rt, "/x/doc.md/view"); m.Page == nil || m.Page.Name != "view" || m.PathParams["b"] != "doc.md" {
		t.Errorf("/x/doc.md/view -> %+v", m)
	}
	if m := matchPath(t, rt, "/x/doc.md/edit"); m.Page == nil || m.Page.Name != "edit" || m.PathParams["a"] != "doc" {
		t.Errorf("/x/doc.md/edit -> %+v", m)
	}
}

// BuildPath escapes the value and keeps the text around it, and what it builds
// matches back to the same value.
func TestBuildPath_Affixed(t *testing.T) {
	rt := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	if err := rt.RegisterDocument(testDocument("md", "/blogs/{slug}.md")); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"hello", "çay ?#%", "...", "a.md"} {
		path, err := BuildPath("/blogs/{slug}.md", map[string]string{"slug": slug})
		if err != nil {
			t.Fatalf("BuildPath(%q) = %v", slug, err)
		}
		if want := "/blogs/" + url.PathEscape(slug) + ".md"; path != want {
			t.Errorf("BuildPath(%q) = %q, want %q", slug, path, want)
		}
		if m := matchPath(t, rt, path); m.Document == nil || m.PathParams["slug"] != slug {
			t.Errorf("%q -> %q -> %+v", slug, path, m)
		}
	}
	for _, slug := range []string{"a/b", "", ".", ".."} {
		if _, err := BuildPath("/blogs/{slug}.md", map[string]string{"slug": slug}); !errors.Is(err, types.ErrRouteParams) {
			t.Errorf("BuildPath(%q) = %v, want ErrRouteParams", slug, err)
		}
	}
	if _, err := BuildPath("/.{x}", map[string]string{"x": "."}); !errors.Is(err, types.ErrRouteParams) {
		t.Errorf(`BuildPath("/.{x}", ".") = %v, want ErrRouteParams`, err)
	}
}

// An affixed placeholder never captures "." or "..": "/blogs/...md" would hand
// {slug}.md the slug "..", which a handler joining it onto a directory climbs out
// with. The segment is the bare placeholder's instead, captured whole.
func TestAffixedPlaceholderNeverCapturesADotSegment(t *testing.T) {
	rt := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	if err := rt.RegisterDocument(testDocument("md", "/blogs/{slug}.md")); err != nil {
		t.Fatal(err)
	}
	for name, pattern := range map[string]string{"post": "/blogs/{slug}", "v": "/x/.{v}"} {
		if err := rt.Register(newTestPage(name, map[string]string{"en": pattern})); err != nil {
			t.Fatal(err)
		}
	}
	for path, want := range map[string]string{
		"/blogs/...md":  "post",
		"/blogs/..md":   "post",
		"/blogs/....md": "md",
		"/x/...":        "",
		"/x/..":         "",
	} {
		m := matchPath(t, rt, path)
		for name, value := range m.PathParams {
			if value == "." || value == ".." {
				t.Errorf("%s captured %s = %q", path, name, value)
			}
		}
		got := ""
		switch {
		case m.Document != nil:
			got = m.Document.Name
		case m.Page != nil:
			got = m.Page.Name
		}
		if got != want {
			t.Errorf("%s -> %q, want %q", path, got, want)
		}
	}
}
