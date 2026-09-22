package router

import (
	"errors"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

func mustParse(t *testing.T, pattern string) []patternSegment {
	t.Helper()
	segments, err := parsePattern(pattern)
	if err != nil {
		t.Fatalf("parsePattern(%q) returned unexpected error: %v", pattern, err)
	}
	return segments
}

func mustInsert(t *testing.T, n *node, pattern string) *node {
	t.Helper()
	target, err := n.insert(mustParse(t, pattern))
	if err != nil {
		t.Fatalf("insert(%q) returned unexpected error: %v", pattern, err)
	}
	return target
}

func TestParsePattern_Valid(t *testing.T) {
	cases := map[string][]patternSegment{
		"/":                {},
		"/blog":            {{kind: segmentStatic, text: "blog"}},
		"/blog/":           {{kind: segmentStatic, text: "blog"}},
		"/blog/{slug}":     {{kind: segmentStatic, text: "blog"}, {kind: segmentDynamic, text: "slug"}},
		"/files/{path...}": {{kind: segmentStatic, text: "files"}, {kind: segmentCatchAll, text: "path"}},
	}
	for pattern, want := range cases {
		got := mustParse(t, pattern)
		if len(got) != len(want) {
			t.Fatalf("parsePattern(%q) = %v, want %v", pattern, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("parsePattern(%q)[%d] = %+v, want %+v", pattern, i, got[i], want[i])
			}
		}
	}
}

func TestParsePattern_CatchAllNotFinal_Invalid(t *testing.T) {
	_, err := parsePattern("/files/{path...}/more")
	if err == nil {
		t.Fatal("expected error for catch-all not in final position")
	}
	if !errors.Is(err, ErrInvalidPattern) {
		t.Fatalf("expected ErrInvalidPattern, got %v", err)
	}
}

func TestParsePattern_MissingLeadingSlash_Invalid(t *testing.T) {
	_, err := parsePattern("blog/post")
	if !errors.Is(err, ErrInvalidPattern) {
		t.Fatalf("expected ErrInvalidPattern, got %v", err)
	}
}

func TestParsePattern_EmptySegment_Invalid(t *testing.T) {
	_, err := parsePattern("/blog//post")
	if !errors.Is(err, ErrInvalidPattern) {
		t.Fatalf("expected ErrInvalidPattern for empty segment, got %v", err)
	}
}

func TestParsePattern_EmptyPlaceholderName_Invalid(t *testing.T) {
	_, err := parsePattern("/blog/{}")
	if !errors.Is(err, ErrInvalidPattern) {
		t.Fatalf("expected ErrInvalidPattern for empty placeholder name, got %v", err)
	}
}

func TestParsePattern_EmptyCatchAllName_Invalid(t *testing.T) {
	_, err := parsePattern("/files/{...}")
	if !errors.Is(err, ErrInvalidPattern) {
		t.Fatalf("expected ErrInvalidPattern for empty catch-all name, got %v", err)
	}
}

func TestNode_Match_Static(t *testing.T) {
	tree := &node{}
	target := mustInsert(t, tree, "/blog/post")
	target.page = &types.Page{Name: "post"}

	got, params, ok := tree.match([]string{"blog", "post"})
	if !ok || got != target {
		t.Fatalf("match static: ok=%v got=%v want=%v", ok, got, target)
	}
	if len(params) != 0 {
		t.Fatalf("static match should capture no params, got %v", params)
	}
}

func TestNode_Match_Dynamic(t *testing.T) {
	tree := &node{}
	target := mustInsert(t, tree, "/blog/{slug}")
	target.page = &types.Page{Name: "dynamic"}

	got, params, ok := tree.match([]string{"blog", "hello-world"})
	if !ok || got != target {
		t.Fatalf("match dynamic: ok=%v got=%v", ok, got)
	}
	if params["slug"] != "hello-world" {
		t.Fatalf("params[slug] = %q, want %q", params["slug"], "hello-world")
	}
}

func TestNode_Match_NestedDynamicSegments(t *testing.T) {
	tree := &node{}
	target := mustInsert(t, tree, "/users/{id}/posts/{postID}")
	target.page = &types.Page{Name: "user-post"}

	got, params, ok := tree.match([]string{"users", "42", "posts", "99"})
	if !ok || got != target {
		t.Fatalf("nested dynamic match failed: ok=%v", ok)
	}
	if params["id"] != "42" || params["postID"] != "99" {
		t.Fatalf("params = %v, want id=42 postID=99", params)
	}
}

func TestNode_Match_CatchAll(t *testing.T) {
	tree := &node{}
	target := mustInsert(t, tree, "/files/{path...}")
	target.page = &types.Page{Name: "files"}

	got, params, ok := tree.match([]string{"files", "a", "b", "c.txt"})
	if !ok || got != target {
		t.Fatalf("catch-all match failed: ok=%v", ok)
	}
	if params["path"] != "a/b/c.txt" {
		t.Fatalf("params[path] = %q, want %q", params["path"], "a/b/c.txt")
	}
}

// TestNode_Match_StaticBeatsDynamic_Depth2 is the classic radix-router priority
// case named in the task brief: a static "/blog/new" must win over a dynamic
// "/blog/{slug}" for the request "/blog/new", at the first level where they
// diverge.
func TestNode_Match_StaticBeatsDynamic_Depth2(t *testing.T) {
	tree := &node{}
	staticTarget := mustInsert(t, tree, "/blog/new")
	staticTarget.page = &types.Page{Name: "new"}
	dynamicTarget := mustInsert(t, tree, "/blog/{slug}")
	dynamicTarget.page = &types.Page{Name: "slug"}

	got, params, ok := tree.match([]string{"blog", "new"})
	if !ok || got != staticTarget {
		t.Fatalf("expected static route to win, got page=%v ok=%v", got, ok)
	}
	if len(params) != 0 {
		t.Fatalf("static match should capture no params, got %v", params)
	}

	got2, params2, ok2 := tree.match([]string{"blog", "other-post"})
	if !ok2 || got2 != dynamicTarget {
		t.Fatalf("expected dynamic route for non-static segment, got %v", got2)
	}
	if params2["slug"] != "other-post" {
		t.Fatalf("params2[slug] = %q, want other-post", params2["slug"])
	}
}

// TestNode_Match_StaticBeatsDynamic_Depth3 repeats the priority test one level
// deeper, where the divergence between the static and dynamic routes happens at
// the third segment rather than the second.
func TestNode_Match_StaticBeatsDynamic_Depth3(t *testing.T) {
	tree := &node{}
	staticTarget := mustInsert(t, tree, "/blog/{category}/new")
	staticTarget.page = &types.Page{Name: "category-new"}
	dynamicTarget := mustInsert(t, tree, "/blog/{category}/{slug}")
	dynamicTarget.page = &types.Page{Name: "category-slug"}

	got, params, ok := tree.match([]string{"blog", "tech", "new"})
	if !ok || got != staticTarget {
		t.Fatalf("expected static route to win at depth 3, got %v", got)
	}
	if params["category"] != "tech" {
		t.Fatalf("params[category] = %q, want tech", params["category"])
	}

	got2, params2, ok2 := tree.match([]string{"blog", "tech", "hello-world"})
	if !ok2 || got2 != dynamicTarget {
		t.Fatalf("expected dynamic route to win for non-static third segment, got %v", got2)
	}
	if params2["category"] != "tech" || params2["slug"] != "hello-world" {
		t.Fatalf("params2 = %v, want category=tech slug=hello-world", params2)
	}
}

// TestNode_Match_BacktracksWhenStaticCannotTerminate covers the failure mode
// behind the "static beats dynamic" rule: a static child can exist at a level
// purely because some other, longer pattern passes through it, without being
// itself a registered route. Matching must backtrack past that non-terminal
// static child and try the dynamic edge, rather than failing outright because a
// static edge existed.
func TestNode_Match_BacktracksWhenStaticCannotTerminate(t *testing.T) {
	tree := &node{}
	deepTarget := mustInsert(t, tree, "/blog/{category}/archive/full")
	deepTarget.page = &types.Page{Name: "archive-full"}
	shallowTarget := mustInsert(t, tree, "/blog/{category}/{slug}")
	shallowTarget.page = &types.Page{Name: "category-slug"}

	// "archive" exists as a static child under {category} (from the four-segment
	// pattern) but is not itself terminal there, so the three-segment request
	// must fall back to the dynamic {slug} edge.
	got, params, ok := tree.match([]string{"blog", "tech", "archive"})
	if !ok || got != shallowTarget {
		t.Fatalf("expected backtrack to dynamic route, got %v ok=%v", got, ok)
	}
	if params["category"] != "tech" || params["slug"] != "archive" {
		t.Fatalf("params = %v, want category=tech slug=archive", params)
	}

	// The full four-segment path still resolves to the deeper static route.
	got2, _, ok2 := tree.match([]string{"blog", "tech", "archive", "full"})
	if !ok2 || got2 != deepTarget {
		t.Fatalf("expected deep static route to still match, got %v ok=%v", got2, ok2)
	}
}

func TestNode_Match_NoMatch(t *testing.T) {
	tree := &node{}
	target := mustInsert(t, tree, "/blog/post")
	target.page = &types.Page{Name: "post"}

	if _, _, ok := tree.match([]string{"blog", "missing"}); ok {
		t.Fatal("expected no match for unregistered segment")
	}
	if _, _, ok := tree.match([]string{"blog"}); ok {
		t.Fatal("expected no match for a non-terminal prefix")
	}
	if _, _, ok := tree.match(nil); ok {
		t.Fatal("expected no match at an empty tree's root")
	}
}
