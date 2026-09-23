package main

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/examples/magazine/newsroom"
	"github.com/Elagoht/collage/pkg/collage"
)

// backend is a controllable stand-in for the newsroom API. Every test that needs a
// particular failure builds one of these rather than driving the real server's
// chaos knob: the schedule there is shared across every endpoint, so "the sidebar
// is down but the article is fine" cannot be expressed with it, and the client's
// retry masks an intermittent schedule entirely.
type backend struct {
	store *newsroom.Store
	// failPrefixes lists path prefixes that answer 503 instead of data.
	failPrefixes []string
	// hits counts requests per path prefix, so a test can prove a render happened
	// again rather than being served from the page cache.
	hits atomic.Int64
}

func newBackend(t *testing.T, failPrefixes ...string) (*backend, *httptest.Server) {
	t.Helper()
	store, err := newsroom.NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	b := &backend{store: store, failPrefixes: failPrefixes}
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	return b, srv
}

func (b *backend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.hits.Add(1)

	for _, prefix := range b.failPrefixes {
		if strings.HasPrefix(r.URL.Path, prefix) {
			http.Error(w, "injected", http.StatusServiceUnavailable)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)

	switch {
	case r.URL.Path == "/healthz":
		enc.Encode(map[string]string{"status": "ok"})
	case r.URL.Path == "/v1/categories":
		enc.Encode(b.store.Categories())
	case r.URL.Path == "/v1/popular":
		enc.Encode(b.store.Popular(5))
	case r.URL.Path == "/v1/articles":
		q := r.URL.Query()
		enc.Encode(b.store.List(newsroom.Filter{
			Category: q.Get("category"), Author: q.Get("author"), Query: q.Get("q"),
		}))
	case strings.HasPrefix(r.URL.Path, "/v1/articles/"):
		art, err := b.store.Article(strings.TrimPrefix(r.URL.Path, "/v1/articles/"))
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		enc.Encode(art)
	case strings.HasPrefix(r.URL.Path, "/v1/categories/"):
		cat, err := b.store.Category(strings.TrimPrefix(r.URL.Path, "/v1/categories/"))
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		enc.Encode(cat)
	case strings.HasPrefix(r.URL.Path, "/v1/authors/"):
		author, err := b.store.Author(strings.TrimPrefix(r.URL.Path, "/v1/authors/"))
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		enc.Encode(author)
	default:
		http.Error(w, "no route", http.StatusNotFound)
	}
}

// newTestSite builds the site against api, with caching left on. The cache matters
// to several tests here — degraded renders must not enter it — so switching it off
// would remove the behaviour under test.
func newTestSite(t *testing.T, apiURL string) http.Handler {
	t.Helper()
	app, _, err := newSite(config{
		Host:          "localhost",
		Port:          3000,
		APIBaseURL:    apiURL,
		PublicBaseURL: "https://thewire.example",
		CacheTTL:      time.Minute,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("newSite() error = %v", err)
	}
	return app.Handler()
}

func fetch(t *testing.T, h http.Handler, target string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec, rec.Body.String()
}

func TestSite_EveryRouteAnswers(t *testing.T) {
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	for _, tc := range []struct {
		target string
		status int
		ctype  string
	}{
		{"/", 200, "text/html"},
		{"/tr/", 200, "text/html"},
		{"/category/technology", 200, "text/html"},
		{"/tr/kategori/technology", 200, "text/html"},
		{"/author/noor-haddad", 200, "text/html"},
		{"/tr/yazar/noor-haddad", 200, "text/html"},
		{"/2026/09/seawalls-buy-time-not-safety", 200, "text/html"},
		{"/search?q=grid", 200, "text/html"},
		{"/tr/arama?q=grid", 200, "text/html"},
		{"/rss.xml", 200, "application/rss+xml"},
		{"/sitemap.xml", 200, "application/xml"},
		{"/robots.txt", 200, "text/plain"},
		{"/healthz", 200, "application/json"},
		{"/static/magazine.css", 200, "text/css"},
		{"/category/no-such-category", 404, "text/html"},
		{"/nothing/here/at-all", 404, "text/html"},
	} {
		rec, _ := fetch(t, site, tc.target)
		if rec.Code != tc.status {
			t.Errorf("GET %s = %d, want %d", tc.target, rec.Code, tc.status)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tc.ctype) {
			t.Errorf("GET %s Content-Type = %q, want %q", tc.target, ct, tc.ctype)
		}
	}
}

func TestSite_LocalePathsAreNotInterchangeable(t *testing.T) {
	// The Turkish path without the "/tr" prefix must 404. Locale is resolved from
	// the URL alone, so "/kategori/technology" is not a route in the English tree
	// and must not quietly become one — otherwise the same page has two URLs, two
	// cache entries and two entries in a crawler's index.
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	if rec, _ := fetch(t, site, "/kategori/technology"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /kategori/technology = %d, want 404", rec.Code)
	}
	if rec, _ := fetch(t, site, "/tr/category/technology"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /tr/category/technology = %d, want 404", rec.Code)
	}
}

func TestSite_TurkishPageLinksTurkishURLs(t *testing.T) {
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	_, body := fetch(t, site, "/tr/")
	if !strings.Contains(body, `href="/tr/kategori/technology"`) {
		t.Error("the Turkish front page does not link Turkish category URLs")
	}
	if strings.Contains(body, `href="/category/`) {
		t.Error("the Turkish front page links an English category URL, which 404s in this locale")
	}
}

func TestSite_ArticleDateMustMatchTheArticle(t *testing.T) {
	// Without the check, every article answers at every date: a duplicate-content
	// problem for crawlers and an unbounded set of cache keys for one piece.
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	if rec, _ := fetch(t, site, "/2026/09/seawalls-buy-time-not-safety"); rec.Code != http.StatusOK {
		t.Fatalf("the correct date = %d, want 200", rec.Code)
	}
	rec, body := fetch(t, site, "/2001/01/seawalls-buy-time-not-safety")
	if rec.Code != http.StatusNotFound {
		t.Errorf("a wrong date = %d, want 404", rec.Code)
	}
	if !strings.Contains(body, "could not be found") {
		t.Error("the article's own 404 page did not render; the page-specific not-found page is not wired")
	}
}

func TestSite_SidebarFailureDegradesRatherThanFails(t *testing.T) {
	// The reason the sidebar is a separate, non-required fragment with a fallback.
	// The reader still gets the piece they came for.
	_, api := newBackend(t, "/v1/popular")
	site := newTestSite(t, api.URL)

	rec, body := fetch(t, site, "/2026/09/seawalls-buy-time-not-safety")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a failing sidebar must not take the article down", rec.Code)
	}
	if !strings.Contains(body, "Seawalls Buy Time") {
		t.Error("the article did not render")
	}
	if !strings.Contains(body, "panel-degraded") {
		t.Error("the sidebar's fallback did not render")
	}
	if strings.Contains(body, `class="ranked"`) {
		t.Error("the real sidebar rendered despite the backend refusing it")
	}
}

func TestSite_NavFailureKeepsTheRestOfTheChrome(t *testing.T) {
	_, api := newBackend(t, "/v1/categories")
	site := newTestSite(t, api.URL)

	rec, body := fetch(t, site, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(body, "sections-empty") {
		t.Error("the nav fallback did not render")
	}
	if !strings.Contains(body, "The Wire") {
		t.Error("the masthead is gone; the layout's own data handler must not depend on the backend")
	}
}

func TestSite_DegradedRendersAreNotCached(t *testing.T) {
	// The framework declines to cache a render in which any fragment failed. That
	// is what stops a momentary outage from being frozen into the page for the
	// whole TTL — the sidebar comes back on the next request instead.
	b, api := newBackend(t, "/v1/popular")
	site := newTestSite(t, api.URL)

	target := "/2026/09/seawalls-buy-time-not-safety"
	if rec, _ := fetch(t, site, target); rec.Code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", rec.Code)
	}
	after := b.hits.Load()

	if rec, _ := fetch(t, site, target); rec.Code != http.StatusOK {
		t.Fatalf("second request = %d, want 200", rec.Code)
	}
	if b.hits.Load() == after {
		t.Error("the second request reached no backend, so the degraded page was cached; a momentary outage would persist for the whole TTL")
	}
}

func TestSite_HealthyRendersAreCached(t *testing.T) {
	// The mirror image, and the reason the test above proves anything: with a
	// healthy backend the second request must NOT reach it.
	b, api := newBackend(t)
	site := newTestSite(t, api.URL)

	target := "/2026/09/seawalls-buy-time-not-safety"
	fetch(t, site, target)
	after := b.hits.Load()
	fetch(t, site, target)

	if b.hits.Load() != after {
		t.Errorf("backend hits went %d -> %d; a healthy render must be served from the cache on the second request", after, b.hits.Load())
	}
}

func TestSite_SearchIsNeverCached(t *testing.T) {
	// The page's cache key includes the raw query string, so caching it would mint
	// an entry per distinct "?q=" and let a crawler evict everything else.
	b, api := newBackend(t)
	site := newTestSite(t, api.URL)

	fetch(t, site, "/search?q=grid")
	after := b.hits.Load()
	fetch(t, site, "/search?q=grid")

	if b.hits.Load() == after {
		t.Error("the second identical search reached no backend, so it was cached")
	}
}

func TestSite_MissingArticleIs404NotAnError(t *testing.T) {
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	rec, _ := fetch(t, site, "/2026/09/no-such-piece")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (the client must keep ErrNotFound apart from an outage)", rec.Code)
	}
}

func TestSite_BackendOutageOnARequiredFragmentIs500(t *testing.T) {
	// The other half of the same distinction: an unreachable backend must not be
	// reported as a missing article, or a crawler concludes the archive was deleted.
	_, api := newBackend(t, "/v1/articles")
	site := newTestSite(t, api.URL)

	rec, body := fetch(t, site, "/2026/09/seawalls-buy-time-not-safety")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(body, "cannot be produced") {
		t.Error("the site's 500 page did not render")
	}
}

func TestSite_FeedIsWellFormedXMLWithAbsoluteLinks(t *testing.T) {
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	_, body := fetch(t, site, "/rss.xml")

	var feed rss
	if err := xml.Unmarshal([]byte(body), &feed); err != nil {
		t.Fatalf("the feed is not well-formed XML: %v", err)
	}
	if len(feed.Channel.Items) == 0 {
		t.Fatal("the feed carries no items")
	}
	for _, it := range feed.Channel.Items {
		if !strings.HasPrefix(it.Link, "https://thewire.example/") {
			t.Errorf("item link = %q, want an absolute URL built from PublicBaseURL; a feed reader cannot follow a relative one", it.Link)
		}
	}
}

func TestSite_SitemapListsBothLocales(t *testing.T) {
	// Nothing links the Turkish pages except the language toggle, so the sitemap is
	// how a crawler discovers them at all.
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	_, body := fetch(t, site, "/sitemap.xml")

	var set urlset
	if err := xml.Unmarshal([]byte(body), &set); err != nil {
		t.Fatalf("the sitemap is not well-formed XML: %v", err)
	}

	var en, tr int
	for _, u := range set.URLs {
		if strings.Contains(u.Loc, "/tr/") {
			tr++
		} else {
			en++
		}
	}
	if en == 0 || tr == 0 {
		t.Errorf("sitemap holds %d English and %d Turkish URLs, want both", en, tr)
	}
}

func TestSite_HealthReportsADegradedBackendWithout503(t *testing.T) {
	// Liveness, not readiness: the site is still serving cached pages, error pages
	// and static files, so an orchestrator restarting it for an upstream outage
	// would be restarting the wrong process.
	_, api := newBackend(t, "/healthz")
	site := newTestSite(t, api.URL)

	rec, body := fetch(t, site, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var status map[string]string
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status["status"] != "degraded" || status["newsroom"] != "unreachable" {
		t.Errorf("body = %v, want status degraded and newsroom unreachable", status)
	}
}

func TestSite_InvalidatingATagDropsTheCachedPage(t *testing.T) {
	// Every listing and the feed declare "articles", so publishing invalidates all
	// of them with one call.
	b, api := newBackend(t)
	app, _, err := newSite(config{
		Host: "localhost", Port: 3000, APIBaseURL: api.URL,
		PublicBaseURL: "https://thewire.example", CacheTTL: time.Minute,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("newSite() error = %v", err)
	}
	handler := app.Handler()

	fetch(t, handler, "/")
	cached := b.hits.Load()
	fetch(t, handler, "/")
	if b.hits.Load() != cached {
		t.Fatalf("the second request was not served from the cache")
	}

	if err := app.InvalidateTags(t.Context(), "articles"); err != nil {
		t.Fatalf("InvalidateTags() error = %v", err)
	}

	fetch(t, handler, "/")
	if b.hits.Load() == cached {
		t.Error("the request after invalidation was still served from the cache")
	}
}

// compile-time check that the site builder returns the framework's App, so a
// refactor cannot quietly change what main receives.
var _ func(config) (*collage.App, *newsroom.Client, error) = newSite
