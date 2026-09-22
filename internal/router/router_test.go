package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

func newTestPage(name string, paths map[string]string) *types.Page {
	return &types.Page{Name: name, Paths: paths}
}

func newTestPageWithRedirects(name string, paths map[string]string, redirects []*types.Redirect) *types.Page {
	page := newTestPage(name, paths)
	page.Redirects = redirects
	return page
}

func mustRegister(t *testing.T, r Router, page *types.Page) {
	t.Helper()
	if err := r.Register(page); err != nil {
		t.Fatalf("Register(%q) returned unexpected error: %v", page.Name, err)
	}
}

func matchPath(t *testing.T, r Router, path string) *MatchResult {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	result, err := r.Match(req)
	if err != nil {
		t.Fatalf("Match(%q) returned unexpected error: %v", path, err)
	}
	return result
}

// TestRouter_StaticBeatsDynamicPageMatch is the end-to-end version of the
// brief's headline priority requirement, through Router.Register and
// Router.Match rather than the tree directly.
func TestRouter_StaticBeatsDynamicPageMatch(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	mustRegister(t, r, newTestPage("blog-slug", map[string]string{"en": "/blog/{slug}"}))
	mustRegister(t, r, newTestPage("blog-new", map[string]string{"en": "/blog/new"}))

	result := matchPath(t, r, "/blog/new")
	if result.IsNotFound || result.Page == nil || result.Page.Name != "blog-new" {
		t.Fatalf("expected static page blog-new, got %+v", result)
	}

	result2 := matchPath(t, r, "/blog/hello")
	if result2.IsNotFound || result2.Page == nil || result2.Page.Name != "blog-slug" {
		t.Fatalf("expected dynamic page blog-slug, got %+v", result2)
	}
	if result2.PathParams["slug"] != "hello" {
		t.Fatalf("PathParams[slug] = %q, want hello", result2.PathParams["slug"])
	}
}

func TestRouter_PercentDecoding(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	mustRegister(t, r, newTestPage("blog-slug", map[string]string{"en": "/blog/{slug}"}))

	result := matchPath(t, r, "/blog/hello%20world")
	if result.IsNotFound {
		t.Fatal("expected a match for a percent-encoded segment")
	}
	if result.PathParams["slug"] != "hello world" {
		t.Fatalf("PathParams[slug] = %q, want %q", result.PathParams["slug"], "hello world")
	}
}

// TestDecodeSegments_FailureIsNotAnError exercises the exact per-segment decode
// step Router.Match uses: a segment that fails to percent-decode reports false
// rather than an error, which Match then turns into IsNotFound rather than a
// 500. This is tested directly against decodeSegments, rather than through a
// constructed *http.Request, because net/url validates percent-encoding while
// parsing a request's path: a real *http.Request (from httptest.NewRequest or
// an actual server) can never carry an invalidly-escaped path to a handler in
// the first place. decodeSegments is what guards Match against any caller that
// does not go through that parsing, so it is verified in isolation.
func TestDecodeSegments_FailureIsNotAnError(t *testing.T) {
	if _, ok := decodeSegments([]string{"blog", "bad%zz"}); ok {
		t.Fatal("expected decodeSegments to report failure for an invalid escape")
	}
}

func TestDecodeSegments_Success(t *testing.T) {
	decoded, ok := decodeSegments([]string{"blog", "hello%20world"})
	if !ok {
		t.Fatal("expected decodeSegments to succeed")
	}
	if decoded[1] != "hello world" {
		t.Fatalf("decoded[1] = %q, want %q", decoded[1], "hello world")
	}
}

// TestRouter_PercentDecoding_EncodedSlashCannotTraverseSegments ensures a
// percent-encoded "/" inside one segment (%2F) cannot be used to smuggle an
// extra path separator: the segment "a%2Fb" must be treated as the single
// literal segment "a/b", not as two segments "a" and "b", so it cannot reach a
// route registered at a different depth.
func TestRouter_PercentDecoding_EncodedSlashCannotTraverseSegments(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	// A two-segment static route that %2F-smuggling might wrongly reach.
	mustRegister(t, r, newTestPage("secret", map[string]string{"en": "/files/secret"}))
	// A one-segment dynamic route matching the encoded request literally.
	mustRegister(t, r, newTestPage("files-name", map[string]string{"en": "/files/{name}"}))

	result := matchPath(t, r, "/files/a%2Fsecret")
	if result.IsNotFound || result.Page == nil {
		t.Fatalf("expected a match against the dynamic route, got %+v", result)
	}
	if result.Page.Name != "files-name" {
		t.Fatalf("expected the %%2F-containing segment to stay within /files/{name}, got page %q", result.Page.Name)
	}
	if result.PathParams["name"] != "a/secret" {
		t.Fatalf("PathParams[name] = %q, want the literal %q", result.PathParams["name"], "a/secret")
	}
}

func TestRouter_TrailingSlashEquivalence(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	mustRegister(t, r, newTestPage("blog-index", map[string]string{"en": "/blog"}))

	withSlash := matchPath(t, r, "/blog/")
	without := matchPath(t, r, "/blog")
	if withSlash.IsNotFound || without.IsNotFound {
		t.Fatalf("expected both /blog and /blog/ to match, got %+v and %+v", withSlash, without)
	}
	if withSlash.Page != without.Page {
		t.Fatal("expected /blog and /blog/ to resolve to the same page")
	}
}

func TestRouter_Root(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	mustRegister(t, r, newTestPage("home", map[string]string{"en": "/"}))

	result := matchPath(t, r, "/")
	if result.IsNotFound || result.Page == nil || result.Page.Name != "home" {
		t.Fatalf("expected root page match, got %+v", result)
	}
}

func TestRouter_Register_CatchAllNotFinal_Rejected(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	err := r.Register(newTestPage("bad", map[string]string{"en": "/files/{path...}/extra"}))
	if !errors.Is(err, ErrInvalidPattern) {
		t.Fatalf("expected ErrInvalidPattern, got %v", err)
	}
}

func TestRouter_Register_DuplicateRoute_Rejected(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	mustRegister(t, r, newTestPage("first", map[string]string{"en": "/blog/post"}))

	err := r.Register(newTestPage("second", map[string]string{"en": "/blog/post"}))
	if !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("expected ErrDuplicateRoute, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "first") || !strings.Contains(err.Error(), "second") {
		t.Fatalf("expected error to name both pages, got %v", err)
	}
}

// TestRouter_Register_AmbiguousParameterName_Rejected covers the correctness
// issue behind sharing a dynamic tree edge across patterns: "/blog/{slug}" and
// "/blog/{category}/new" both want the segment right after "/blog/" to be a
// dynamic edge, but under two different names. Allowing both would mean
// whichever registers second silently has its captured value reported under
// the first pattern's placeholder name instead of its own, so this is rejected
// at registration time instead.
func TestRouter_Register_AmbiguousParameterName_Rejected(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	mustRegister(t, r, newTestPage("blog-slug", map[string]string{"en": "/blog/{slug}"}))

	err := r.Register(newTestPage("blog-category-new", map[string]string{"en": "/blog/{category}/new"}))
	if !errors.Is(err, ErrAmbiguousParameterName) {
		t.Fatalf("expected ErrAmbiguousParameterName, got %v", err)
	}
}

func TestRouter_Register_DuplicateRoute_TrailingSlashNormalized(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	mustRegister(t, r, newTestPage("first", map[string]string{"en": "/blog/"}))

	err := r.Register(newTestPage("second", map[string]string{"en": "/blog"}))
	if !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("expected ErrDuplicateRoute for trailing-slash-equivalent duplicate, got %v", err)
	}
}

func TestRouter_Redirect_MatchWithParamSubstitution(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	page := newTestPageWithRedirects("blog", map[string]string{"en": "/blog/{slug}"}, []*types.Redirect{
		{From: "/old-blog/{slug}", To: "/blog/{slug}", Permanent: true},
	})
	mustRegister(t, r, page)

	result := matchPath(t, r, "/old-blog/hello-world")
	if result.IsNotFound {
		t.Fatal("expected a redirect match")
	}
	if result.Page != nil {
		t.Fatalf("expected Page to be nil for a redirect match, got %+v", result.Page)
	}
	if result.RedirectTo != "/blog/hello-world" {
		t.Fatalf("RedirectTo = %q, want /blog/hello-world", result.RedirectTo)
	}
	if result.RedirectStatus != 301 {
		t.Fatalf("RedirectStatus = %d, want 301", result.RedirectStatus)
	}
}

func TestRouter_Redirect_TakesPriorityOverPage(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	page1 := newTestPage("target", map[string]string{"en": "/new-path"})
	page2 := newTestPageWithRedirects("source", map[string]string{"en": "/other"}, []*types.Redirect{
		{From: "/legacy", To: "/new-path"},
	})
	mustRegister(t, r, page1)
	mustRegister(t, r, page2)

	result := matchPath(t, r, "/legacy")
	if result.Page != nil {
		t.Fatalf("expected redirect to take priority, got page %+v", result.Page)
	}
	if result.RedirectTo != "/new-path" {
		t.Fatalf("RedirectTo = %q, want /new-path", result.RedirectTo)
	}
}

func TestRouter_Redirect_UnsubstitutedPlaceholder_RejectedAtRegistration(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	page := newTestPageWithRedirects("blog", map[string]string{"en": "/blog/{slug}"}, []*types.Redirect{
		{From: "/old-blog/{slug}", To: "/blog/{other}"},
	})
	err := r.Register(page)
	if !errors.Is(err, ErrUnsubstitutedPlaceholder) {
		t.Fatalf("expected ErrUnsubstitutedPlaceholder, got %v", err)
	}
}

func TestRouter_Redirect_ShadowsPage_Rejected(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	mustRegister(t, r, newTestPage("about", map[string]string{"en": "/about"}))

	page := newTestPageWithRedirects("other", map[string]string{"en": "/other"}, []*types.Redirect{
		{From: "/about", To: "/other"},
	})
	err := r.Register(page)
	if !errors.Is(err, ErrRedirectShadowsPage) {
		t.Fatalf("expected ErrRedirectShadowsPage, got %v", err)
	}
}

// TestRouter_Register_PageShadowedByExistingRedirect_Rejected checks the
// reverse registration order from ErrRedirectShadowsPage's headline case: a
// redirect registered first, then a page later claiming the same path. The
// conflict is the same either way, so it is rejected the same way.
func TestRouter_Register_PageShadowedByExistingRedirect_Rejected(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	redirecting := newTestPageWithRedirects("other", map[string]string{"en": "/other"}, []*types.Redirect{
		{From: "/about", To: "/other"},
	})
	mustRegister(t, r, redirecting)

	err := r.Register(newTestPage("about", map[string]string{"en": "/about"}))
	if !errors.Is(err, ErrRedirectShadowsPage) {
		t.Fatalf("expected ErrRedirectShadowsPage, got %v", err)
	}
}

func TestRouter_NoMatch_ReturnsIsNotFoundWithLocale(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en", "tr"}})
	mustRegister(t, r, newTestPage("blog", map[string]string{"en": "/blog"}))

	req := httptest.NewRequest(http.MethodGet, "/tr/does-not-exist", nil)
	result, err := r.Match(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsNotFound {
		t.Fatal("expected IsNotFound for an unregistered path")
	}
	if result.Page != nil {
		t.Fatalf("expected nil Page for a not-found result, got %+v", result.Page)
	}
	if result.Locale != "tr" {
		t.Fatalf("Locale = %q, want tr (still resolved even though nothing matched)", result.Locale)
	}
}

func TestRouter_NotFoundAndErrorPages(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	if r.NotFoundPage() != nil || r.ErrorPage() != nil {
		t.Fatal("expected nil not-found/error pages before registration")
	}

	notFound := newTestPage("404", nil)
	errPage := newTestPage("500", nil)
	if err := r.RegisterNotFound(notFound); err != nil {
		t.Fatalf("RegisterNotFound returned unexpected error: %v", err)
	}
	if err := r.RegisterError(errPage); err != nil {
		t.Fatalf("RegisterError returned unexpected error: %v", err)
	}

	if r.NotFoundPage() != notFound {
		t.Fatal("NotFoundPage did not return the registered page")
	}
	if r.ErrorPage() != errPage {
		t.Fatal("ErrorPage did not return the registered page")
	}
}

// TestRouter_Match_ConcurrentlySafe exercises the read path Router.Match takes
// once registration has finished, the way an HTTP server actually uses it: many
// goroutines calling Match concurrently against a router nothing further
// registers into. It exists so `go test -race` has genuine concurrent access to
// check, not just sequential calls.
func TestRouter_Match_ConcurrentlySafe(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en", "tr"}})
	mustRegister(t, r, newTestPage("blog-new", map[string]string{"en": "/blog/new"}))
	mustRegister(t, r, newTestPage("blog-slug", map[string]string{"en": "/blog/{slug}", "tr": "/blog/{slug}"}))
	mustRegister(t, r, newTestPageWithRedirects("archive", map[string]string{"en": "/archive"}, []*types.Redirect{
		{From: "/old-archive", To: "/archive"},
	}))

	paths := []string{"/blog/new", "/blog/hello", "/tr/blog/merhaba", "/old-archive", "/does-not-exist"}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, p := range paths {
				if _, err := r.Match(httptest.NewRequest(http.MethodGet, p, nil)); err != nil {
					t.Errorf("Match(%q) returned unexpected error: %v", p, err)
				}
			}
		}()
	}
	wg.Wait()
}

func TestRouter_LocaleSpecificPageTrees(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en", "tr"}})
	mustRegister(t, r, newTestPage("blog", map[string]string{
		"en": "/blog/post",
		"tr": "/blog/yazi",
	}))

	enResult := matchPath(t, r, "/blog/post")
	if enResult.IsNotFound || enResult.Locale != "en" {
		t.Fatalf("expected en match, got %+v", enResult)
	}

	trResult := matchPath(t, r, "/tr/blog/yazi")
	if trResult.IsNotFound || trResult.Locale != "tr" {
		t.Fatalf("expected tr match, got %+v", trResult)
	}

	// The English pattern does not exist under the tr locale.
	crossResult := matchPath(t, r, "/tr/blog/post")
	if !crossResult.IsNotFound {
		t.Fatalf("expected no match for an en-only path under locale tr, got %+v", crossResult)
	}
}

// TestRouter_RouteTable exercises Router.Match against a broad table of
// registered routes and request paths, covering static, dynamic, and
// catch-all segments; nested dynamic segments; locale prefixes; redirects; and
// not-found cases, across more than twenty route/request pairs as required by
// the task brief.
func TestRouter_RouteTable(t *testing.T) {
	r := New(LocaleOptions{Default: "en", Supported: []string{"en", "tr"}})

	pages := []*types.Page{
		newTestPage("home", map[string]string{"en": "/", "tr": "/"}),
		newTestPage("about", map[string]string{"en": "/about", "tr": "/hakkinda"}),
		newTestPage("blog-index", map[string]string{"en": "/blog", "tr": "/blog"}),
		newTestPage("blog-new", map[string]string{"en": "/blog/new"}),
		newTestPage("blog-slug", map[string]string{"en": "/blog/{slug}", "tr": "/blog/{slug}"}),
		// These two patterns share the {slug} edge already claimed by blog-slug
		// above, one level deeper: a dynamic edge is only ever reused across
		// patterns when its placeholder name agrees (see
		// TestRouter_Register_AmbiguousParameterName_Rejected), so both reuse
		// the name "slug" rather than introducing a conflicting "category".
		newTestPage("blog-slug-new", map[string]string{"en": "/blog/{slug}/new"}),
		newTestPage("blog-slug-comment", map[string]string{"en": "/blog/{slug}/{commentID}"}),
		newTestPage("user-posts", map[string]string{"en": "/users/{id}/posts/{postID}"}),
		newTestPage("files", map[string]string{"en": "/files/{path...}"}),
		newTestPage("shop-item", map[string]string{"en": "/shop/items/{itemID}"}),
		newTestPageWithRedirects("archive", map[string]string{"en": "/archive"}, []*types.Redirect{
			{From: "/old-archive", To: "/archive", Permanent: true},
			{From: "/legacy/{year}", To: "/archive?year={year}"},
		}),
	}
	for _, page := range pages {
		mustRegister(t, r, page)
	}

	type expectation struct {
		name           string
		path           string
		wantNotFound   bool
		wantPageName   string
		wantLocale     string
		wantParams     map[string]string
		wantRedirectTo string
	}

	cases := []expectation{
		{name: "root en", path: "/", wantPageName: "home", wantLocale: "en"},
		{name: "root tr prefix", path: "/tr", wantPageName: "home", wantLocale: "tr"},
		{name: "about en", path: "/about", wantPageName: "about", wantLocale: "en"},
		{name: "about tr", path: "/tr/hakkinda", wantPageName: "about", wantLocale: "tr"},
		{name: "blog index", path: "/blog", wantPageName: "blog-index", wantLocale: "en"},
		{name: "blog index trailing slash", path: "/blog/", wantPageName: "blog-index", wantLocale: "en"},
		{name: "blog new beats blog slug", path: "/blog/new", wantPageName: "blog-new", wantLocale: "en", wantParams: map[string]string{}},
		{name: "blog slug", path: "/blog/hello-world", wantPageName: "blog-slug", wantLocale: "en", wantParams: map[string]string{"slug": "hello-world"}},
		{name: "blog slug percent-decoded", path: "/blog/hello%20world", wantPageName: "blog-slug", wantLocale: "en", wantParams: map[string]string{"slug": "hello world"}},
		{name: "blog slug tr locale", path: "/tr/blog/merhaba", wantPageName: "blog-slug", wantLocale: "tr", wantParams: map[string]string{"slug": "merhaba"}},
		{name: "blog slug new beats slug comment", path: "/blog/tech/new", wantPageName: "blog-slug-new", wantLocale: "en", wantParams: map[string]string{"slug": "tech"}},
		{name: "blog slug comment", path: "/blog/tech/hello", wantPageName: "blog-slug-comment", wantLocale: "en", wantParams: map[string]string{"slug": "tech", "commentID": "hello"}},
		{name: "nested dynamic segments", path: "/users/42/posts/99", wantPageName: "user-posts", wantLocale: "en", wantParams: map[string]string{"id": "42", "postID": "99"}},
		{name: "catch-all single segment", path: "/files/readme.txt", wantPageName: "files", wantLocale: "en", wantParams: map[string]string{"path": "readme.txt"}},
		{name: "catch-all multi segment", path: "/files/a/b/c.txt", wantPageName: "files", wantLocale: "en", wantParams: map[string]string{"path": "a/b/c.txt"}},
		{name: "shop item", path: "/shop/items/abc123", wantPageName: "shop-item", wantLocale: "en", wantParams: map[string]string{"itemID": "abc123"}},
		{name: "redirect static", path: "/old-archive", wantRedirectTo: "/archive", wantLocale: "en"},
		{name: "redirect with param substitution", path: "/legacy/1999", wantRedirectTo: "/archive?year=1999", wantLocale: "en"},
		{name: "not found unknown path", path: "/does-not-exist", wantNotFound: true, wantLocale: "en"},
		{name: "not found tr unknown path", path: "/tr/does-not-exist", wantNotFound: true, wantLocale: "tr"},
		{name: "not found en-only route under tr", path: "/tr/shop/items/abc123", wantNotFound: true, wantLocale: "tr"},
		{name: "not found extra segment past a static leaf", path: "/about/extra", wantNotFound: true, wantLocale: "en"},
		{name: "unsupported locale prefix falls through", path: "/fr/about", wantNotFound: true, wantLocale: "en"},
		{name: "shop item with encoded slash stays one segment", path: "/shop/items/a%2Fb", wantPageName: "shop-item", wantLocale: "en", wantParams: map[string]string{"itemID": "a/b"}},
	}

	if len(cases) < 20 {
		t.Fatalf("route table has %d cases, want at least 20", len(cases))
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := matchPath(t, r, c.path)

			if result.Locale != c.wantLocale {
				t.Fatalf("Locale = %q, want %q", result.Locale, c.wantLocale)
			}

			if c.wantNotFound {
				if !result.IsNotFound {
					t.Fatalf("expected IsNotFound, got %+v", result)
				}
				return
			}

			if c.wantRedirectTo != "" {
				if result.Page != nil {
					t.Fatalf("expected a redirect, got page %+v", result.Page)
				}
				if result.RedirectTo != c.wantRedirectTo {
					t.Fatalf("RedirectTo = %q, want %q", result.RedirectTo, c.wantRedirectTo)
				}
				return
			}

			if result.IsNotFound || result.Page == nil {
				t.Fatalf("expected page %q, got %+v", c.wantPageName, result)
			}
			if result.Page.Name != c.wantPageName {
				t.Fatalf("Page.Name = %q, want %q", result.Page.Name, c.wantPageName)
			}
			for key, want := range c.wantParams {
				if got := result.PathParams[key]; got != want {
					t.Fatalf("PathParams[%q] = %q, want %q", key, got, want)
				}
			}
		})
	}
}
