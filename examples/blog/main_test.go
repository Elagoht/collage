package main

import (
	"context"
	"encoding/xml"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

// The distinguishing text of each of the four error pages. The point of every
// assertion below that names one of these is that the *right* page was served:
// a status code alone cannot tell a page-specific 404 from the site-wide one,
// since both answer 404.
const (
	blogNotFoundText   = "No such post"
	blogErrorText      = "This post could not be loaded"
	globalNotFoundText = "Nothing lives at this address."
	globalErrorText    = "The site hit an unexpected error."
)

// liveSlug is a post the seeded store can actually serve.
const liveSlug = "hello-collage"

// blog starts the example application on an httptest server and returns the
// server, the store it serves from, and the app itself.
//
// It goes through app.Handler(), which is what the real server serves and what
// runs plugin Init, so a startup failure — an unregistered error page, a plugin
// that refused to start — surfaces here as a 503 on the first request rather
// than being skipped in a test-only code path.
func blog(t *testing.T) (*httptest.Server, *PostStore, *collage.App) {
	t.Helper()

	app, store, err := newBlog(templateRoot)
	if err != nil {
		t.Fatalf("newBlog: %v", err)
	}

	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return server, store, app
}

// get issues a GET for path and returns the status, the body, and the response
// headers. Redirects are not followed: this test asserts on the redirect itself,
// not on where it lands.
func get(t *testing.T, server *httptest.Server, path string) (int, string, http.Header) {
	t.Helper()

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: read body: %v", path, err)
	}
	return resp.StatusCode, string(body), resp.Header
}

// assertContains fails the test when body does not contain want.
func assertContains(t *testing.T, path, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Errorf("GET %s: body does not contain %q\n---\n%s\n---", path, want, body)
	}
}

// assertMissing fails the test when body contains unwanted.
func assertMissing(t *testing.T, path, body, unwanted string) {
	t.Helper()
	if strings.Contains(body, unwanted) {
		t.Errorf("GET %s: body unexpectedly contains %q\n---\n%s\n---", path, unwanted, body)
	}
}

// TestHomeRendersThePostIndex proves the whole composition path: the shared
// layout renders with its own data, the content fragment renders inside its
// slot, and the store's posts reach the template.
func TestHomeRendersThePostIndex(t *testing.T) {
	server, _, _ := blog(t)

	status, body, header := get(t, server, "/")
	if status != http.StatusOK {
		t.Fatalf("GET /: status = %d, want %d\n%s", status, http.StatusOK, body)
	}

	assertContains(t, "/", body, site.Name)        // The layout rendered.
	assertContains(t, "/", body, site.Tagline)     // ... including its footer.
	assertContains(t, "/", body, "Hello, collage") // The content fragment rendered inside it.
	assertContains(t, "/", body, `href="/blog/`+liveSlug+`"`)

	if got := header.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("GET /: Content-Type = %q", got)
	}
}

// TestPostRenders proves a dynamic path parameter reaches the data handler and
// the fetched post reaches the template.
func TestPostRenders(t *testing.T) {
	server, store, _ := blog(t)

	path := "/blog/" + liveSlug
	status, body, _ := get(t, server, path)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want %d\n%s", path, status, http.StatusOK, body)
	}

	post, err := store.Post(context.Background(), liveSlug)
	if err != nil {
		t.Fatalf("store.Post: %v", err)
	}
	assertContains(t, path, body, post.Title)
	assertContains(t, path, body, post.Body)
}

// TestUnknownSlugServesTheBlogsOwn404 is the reason types.ErrNotFound exists. The
// store reports a missing post by wrapping collage.ErrNotFound; the framework
// classifies that as "this content does not exist" rather than "something broke",
// answers 404 instead of 500, and serves the page's own NotFoundPage rather than
// the site-wide one.
func TestUnknownSlugServesTheBlogsOwn404(t *testing.T) {
	server, _, _ := blog(t)

	path := "/blog/no-such-post"
	status, body, _ := get(t, server, path)
	if status != http.StatusNotFound {
		t.Fatalf("GET %s: status = %d, want %d\n%s", path, status, http.StatusNotFound, body)
	}

	assertContains(t, path, body, blogNotFoundText)
	assertMissing(t, path, body, globalNotFoundText)
	// The blog's 404 page shares the site layout, so the chrome is there too.
	assertContains(t, path, body, site.Name)
}

// TestFailingPostServesTheBlogsOwn500 is the other half of that distinction: an
// ordinary failure from the same data handler, on the same page, produces a 500
// and the page's own ErrorPage.
func TestFailingPostServesTheBlogsOwn500(t *testing.T) {
	server, _, _ := blog(t)

	path := "/blog/" + BrokenSlug
	status, body, _ := get(t, server, path)
	if status != http.StatusInternalServerError {
		t.Fatalf("GET %s: status = %d, want %d\n%s", path, status, http.StatusInternalServerError, body)
	}

	assertContains(t, path, body, blogErrorText)
	assertMissing(t, path, body, globalErrorText)
	assertMissing(t, path, body, blogNotFoundText)
}

// TestUnmatchedPathServesTheSiteWide404 proves the page-specific 404 above was a
// genuine choice rather than the only 404 registered.
func TestUnmatchedPathServesTheSiteWide404(t *testing.T) {
	server, _, _ := blog(t)

	path := "/no/such/section"
	status, body, _ := get(t, server, path)
	if status != http.StatusNotFound {
		t.Fatalf("GET %s: status = %d, want %d\n%s", path, status, http.StatusNotFound, body)
	}

	assertContains(t, path, body, globalNotFoundText)
	assertMissing(t, path, body, blogNotFoundText)
}

// TestRedirects covers both redirect shapes registered on the post page: the
// permanent one answers 301 and the temporary one 302, and both substitute the
// captured slug into the destination.
func TestRedirects(t *testing.T) {
	server, _, _ := blog(t)

	cases := []struct {
		path   string
		status int
	}{
		{"/old-blog/" + liveSlug, http.StatusMovedPermanently},
		{"/temp-blog/" + liveSlug, http.StatusFound},
	}

	for _, tc := range cases {
		status, body, header := get(t, server, tc.path)
		if status != tc.status {
			t.Errorf("GET %s: status = %d, want %d\n%s", tc.path, status, tc.status, body)
		}
		if got, want := header.Get("Location"), "/blog/"+liveSlug; got != want {
			t.Errorf("GET %s: Location = %q, want %q", tc.path, got, want)
		}
	}
}

// TestInvalidateTagsCausesARerender is the incremental-cache test. A status code
// cannot tell a cache hit from a re-render — both are 200 with the same body — so
// the assertion is on the store's load counter, which only advances when the data
// handler actually runs.
func TestInvalidateTagsCausesARerender(t *testing.T) {
	server, store, app := blog(t)

	path := "/blog/" + liveSlug

	if status, body, _ := get(t, server, path); status != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want %d\n%s", path, status, http.StatusOK, body)
	}
	if got := store.Loads(liveSlug); got != 1 {
		t.Fatalf("after the first request, store.Loads(%q) = %d, want 1", liveSlug, got)
	}

	if status, body, _ := get(t, server, path); status != http.StatusOK {
		t.Fatalf("GET %s (second): status = %d, want %d\n%s", path, status, http.StatusOK, body)
	}
	if got := store.Loads(liveSlug); got != 1 {
		t.Fatalf("after the second request, store.Loads(%q) = %d, want 1: the response was re-rendered instead of served from the incremental cache", liveSlug, got)
	}

	if err := app.InvalidateTags(context.Background(), "post:"+liveSlug); err != nil {
		t.Fatalf("InvalidateTags: %v", err)
	}

	status, body, _ := get(t, server, path)
	if status != http.StatusOK {
		t.Fatalf("GET %s (after invalidation): status = %d, want %d\n%s", path, status, http.StatusOK, body)
	}
	if got := store.Loads(liveSlug); got != 2 {
		t.Fatalf("after invalidating post:%s, store.Loads(%q) = %d, want 2: the invalidated entry was served from cache anyway", liveSlug, liveSlug, got)
	}
	// The re-render is a real render, so the plugin ran over it again.
	assertContains(t, path, body, StampMarker)
}

// TestInvalidateTagsReportsWhatItReached proves InvalidateTagsN's count is the
// number of cache keys the tag resolved to, not a fixed value: the home page and
// the post page both declare "blog:posts", so one invalidation reaches both once
// both have been rendered.
func TestInvalidateTagsReportsWhatItReached(t *testing.T) {
	server, _, app := blog(t)

	if status, body, _ := get(t, server, "/"); status != http.StatusOK {
		t.Fatalf("GET /: status = %d, want %d\n%s", status, http.StatusOK, body)
	}
	if status, body, _ := get(t, server, "/blog/"+liveSlug); status != http.StatusOK {
		t.Fatalf("GET /blog/%s: status = %d, want %d\n%s", liveSlug, status, http.StatusOK, body)
	}

	reached, err := app.InvalidateTagsN(context.Background(), "blog:posts")
	if err != nil {
		t.Fatalf("InvalidateTagsN: %v", err)
	}
	if reached != 2 {
		t.Errorf("InvalidateTagsN(\"blog:posts\") = %d, want 2 (the home page and the post page)", reached)
	}
}

// TestPluginPostProcessesEveryRender proves the plugin path runs end to end: the
// registry discovered OnAfterRender by type assertion, the hook replaced the
// rendered HTML, and the replacement is what the handler served.
func TestPluginPostProcessesEveryRender(t *testing.T) {
	server, _, _ := blog(t)

	for _, path := range []string{"/", "/blog/" + liveSlug} {
		status, body, _ := get(t, server, path)
		if status != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want %d\n%s", path, status, http.StatusOK, body)
		}
		assertContains(t, path, body, StampMarker)
		if !strings.Contains(body, StampMarker+"</body>") {
			t.Errorf("GET %s: marker was not injected in front of </body>", path)
		}
	}
}

// TestPluginRegistersItsCommand proves the other half of the plugin contract: Init
// ran with the application as its Host, and the command it registered through that
// narrow interface reached the application's command list.
func TestPluginRegistersItsCommand(t *testing.T) {
	_, _, app := blog(t)

	commands := app.Commands()
	if len(commands) != 1 {
		t.Fatalf("app.Commands() = %d commands, want 1", len(commands))
	}
	if commands[0].Name != "posts" {
		t.Errorf("app.Commands()[0].Name = %q, want %q", commands[0].Name, "posts")
	}
	if commands[0].Run == nil {
		t.Error("app.Commands()[0].Run is nil")
	}
}

// TestLocalePrefixReachesTheSamePages proves the shared layout and the per-locale
// paths work together: "/tr/blog/{slug}" resolves the Turkish locale from the path
// prefix and reaches the same page.
func TestLocalePrefixReachesTheSamePages(t *testing.T) {
	server, _, _ := blog(t)

	path := "/tr/blog/" + liveSlug
	status, body, _ := get(t, server, path)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want %d\n%s", path, status, http.StatusOK, body)
	}
	assertContains(t, path, body, "Hello, collage")
}

// TestSitemapRendersAndCarriesItsContentType proves the document path end to
// end: the sitemap registered into the same router the pages did, its handler's
// bytes reached the client unchanged, and the Content-Type it was declared with
// is the one written — not the text/html the framework writes for every page.
func TestSitemapRendersAndCarriesItsContentType(t *testing.T) {
	server, store, _ := blog(t)

	status, body, header := get(t, server, "/sitemap.xml")
	if status != http.StatusOK {
		t.Fatalf("GET /sitemap.xml: status = %d, want %d\n%s", status, http.StatusOK, body)
	}
	if got := header.Get("Content-Type"); got != sitemapContentType {
		t.Errorf("GET /sitemap.xml: Content-Type = %q, want %q", got, sitemapContentType)
	}

	// Every post the store lists is in the sitemap, at its absolute URL.
	for _, post := range store.List() {
		assertContains(t, "/sitemap.xml", body, "<loc>"+siteBaseURL+"/blog/"+post.Slug+"</loc>")
	}
	assertContains(t, "/sitemap.xml", body, "<loc>"+siteBaseURL+"/</loc>")

	// A document renders no templates, so nothing the layout contributes — and
	// nothing the plugin's OnAfterRender stamps onto a page — can reach it.
	assertMissing(t, "/sitemap.xml", body, "<html")
	assertMissing(t, "/sitemap.xml", body, StampMarker)

	// The body really is the XML the handler produced: it parses.
	var parsed struct {
		URLs []struct {
			Location string `xml:"loc"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("GET /sitemap.xml: body is not well-formed XML: %v\n%s", err, body)
	}
	if want := len(store.List()) + 1; len(parsed.URLs) != want {
		t.Errorf("GET /sitemap.xml: %d <url> entries, want %d", len(parsed.URLs), want)
	}
}

// TestSitemapRegeneratesAfterInvalidateTags is the document half of the
// incremental-cache test. As with a page, a status code cannot tell a cache hit
// from a re-render — both are 200 with the same body — so the assertion is on the
// store's counter, which only advances when the document's handler actually runs.
func TestSitemapRegeneratesAfterInvalidateTags(t *testing.T) {
	server, store, app := blog(t)

	if status, body, _ := get(t, server, "/sitemap.xml"); status != http.StatusOK {
		t.Fatalf("GET /sitemap.xml: status = %d, want %d\n%s", status, http.StatusOK, body)
	}
	if got := store.SitemapRenders(); got != 1 {
		t.Fatalf("after the first request, store.SitemapRenders() = %d, want 1", got)
	}

	if status, body, _ := get(t, server, "/sitemap.xml"); status != http.StatusOK {
		t.Fatalf("GET /sitemap.xml (second): status = %d, want %d\n%s", status, http.StatusOK, body)
	}
	if got := store.SitemapRenders(); got != 1 {
		t.Fatalf("after the second request, store.SitemapRenders() = %d, want 1: the document was regenerated instead of served from the incremental cache", got)
	}

	if err := app.InvalidateTags(context.Background(), "blog:posts"); err != nil {
		t.Fatalf("InvalidateTags: %v", err)
	}

	if status, body, _ := get(t, server, "/sitemap.xml"); status != http.StatusOK {
		t.Fatalf("GET /sitemap.xml (after invalidation): status = %d, want %d\n%s", status, http.StatusOK, body)
	}
	if got := store.SitemapRenders(); got != 2 {
		t.Fatalf("after invalidating blog:posts, store.SitemapRenders() = %d, want 2: the invalidated document was served from cache anyway", got)
	}
}

// TestRobotsTxtRenders proves a static document serves exactly the bytes its
// handler returned, under the content type it declared.
func TestRobotsTxtRenders(t *testing.T) {
	server, _, _ := blog(t)

	status, body, header := get(t, server, "/robots.txt")
	if status != http.StatusOK {
		t.Fatalf("GET /robots.txt: status = %d, want %d\n%s", status, http.StatusOK, body)
	}
	if got := header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("GET /robots.txt: Content-Type = %q, want it to start with %q", got, "text/plain")
	}
	if body != robotsBody {
		t.Errorf("GET /robots.txt: body = %q, want %q", body, robotsBody)
	}
	// The directives themselves, so a future edit that empties the constant
	// cannot pass the equality check above by agreeing with itself.
	for _, want := range []string{"User-agent: *", "Disallow: /old-blog/", "Sitemap: " + siteBaseURL + "/sitemap.xml"} {
		assertContains(t, "/robots.txt", body, want)
	}
}

// TestStylesheetIsServedFromTheMount proves the mount serves the embedded file
// byte for byte, types it from its extension, and advertises an ETag — which is a
// content hash rather than a size-and-mtime validator precisely because every
// embed.FS file reports a zero ModTime.
func TestStylesheetIsServedFromTheMount(t *testing.T) {
	server, _, _ := blog(t)

	want, err := fs.ReadFile(assetsFS, "static/app.css")
	if err != nil {
		t.Fatalf("read embedded stylesheet: %v", err)
	}

	status, body, header := get(t, server, stylesheetPath)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want %d\n%s", stylesheetPath, status, http.StatusOK, body)
	}
	if got := header.Get("Content-Type"); !strings.HasPrefix(got, "text/css") {
		t.Errorf("GET %s: Content-Type = %q, want it to start with %q", stylesheetPath, got, "text/css")
	}
	if body != string(want) {
		t.Errorf("GET %s: body does not match the embedded file", stylesheetPath)
	}
	if header.Get("ETag") == "" {
		t.Errorf("GET %s: no ETag; a zero-ModTime embed.FS file has nothing else to revalidate with", stylesheetPath)
	}
	if got := header.Get("Cache-Control"); got == "" {
		t.Errorf("GET %s: no Cache-Control; a mounted file never enters the page cache, so this header is all a client has", stylesheetPath)
	}
}

// TestUnknownAssetIsPlainTextNotHTML proves a missing asset is answered by the
// mount, not by the page router: a stylesheet that 404s must not come back as a
// web page. The mount claims its whole prefix, so the request never reaches the
// site-wide not-found page.
func TestUnknownAssetIsPlainTextNotHTML(t *testing.T) {
	server, _, _ := blog(t)

	path := assetPrefix + "nope.css"
	status, body, header := get(t, server, path)
	if status != http.StatusNotFound {
		t.Fatalf("GET %s: status = %d, want %d\n%s", path, status, http.StatusNotFound, body)
	}
	if got := header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("GET %s: Content-Type = %q, want it to start with %q", path, got, "text/plain")
	}
	assertMissing(t, path, body, "<html")
	assertMissing(t, path, body, globalNotFoundText)
}

// TestLocalePrefixedDocument executes the corrected form of the documentation's
// locale example. The feed registers the same pattern, "/feed.xml", under both
// "en" and "tr", and both of these URLs must reach it:
//
//   - "/feed.xml", whose locale resolves from the header, the cookie, or the
//     default;
//   - "/tr/feed.xml", whose "/tr" segment is stripped by path-locale resolution
//     before the router matches, leaving "/feed.xml" to match in the tr tree.
//
// docs/documents.md used to show WithPath("tr", "/tr/feed.xml"), which registers
// a route reached only by "/tr/tr/feed.xml" and answers 404 at the URL the
// example was demonstrating. This test is here so the corrected form is executed
// rather than merely written.
func TestLocalePrefixedDocument(t *testing.T) {
	server, _, _ := blog(t)

	for _, path := range []string{"/feed.xml", "/tr/feed.xml"} {
		status, body, header := get(t, server, path)
		if status != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want %d — a locale prefix is stripped before matching, so the "+
				"tr tree holds %q, not %q\n%s", path, status, http.StatusOK, "/feed.xml", "/tr/feed.xml", body)
		}
		if got := header.Get("Content-Type"); got != feedContentType {
			t.Errorf("GET %s: Content-Type = %q, want %q", path, got, feedContentType)
		}
		if !strings.Contains(body, "<rss version=\"2.0\">") {
			t.Errorf("GET %s: body is not the RSS document: %s", path, body)
		}
	}

	// The two URLs are two locales, not one document served twice: the handler
	// reads rc.Locale, and the locale is part of the cache key, so the bodies
	// differ.
	_, english, _ := get(t, server, "/feed.xml")
	_, turkish, _ := get(t, server, "/tr/feed.xml")
	assertContains(t, "/feed.xml", english, "Collage Blog (en)")
	assertContains(t, "/tr/feed.xml", turkish, "Collage Blog (tr)")

	// And the prefixed spelling the documentation used to show really is a 404,
	// which is why the example had to change rather than the framework.
	if status, _, _ := get(t, server, "/tr/tr/feed.xml"); status != http.StatusNotFound {
		t.Errorf("GET /tr/tr/feed.xml: status = %d, want %d", status, http.StatusNotFound)
	}
}
