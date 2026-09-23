package main

import (
	"encoding/json"
	"encoding/xml"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// The corpus these tests run against.
//
// It is declared here rather than borrowed from the API's own content, because the
// site is a separate program: its tests must not break when someone edits an
// article in a backend it merely talks to. Everything the assertions below name
// lives in this slice.
func testCorpus() ([]Article, []Category, []Author) {
	at := func(iso string) time.Time {
		parsed, err := time.Parse(time.RFC3339, iso)
		if err != nil {
			panic(err)
		}
		return parsed
	}
	articles := []Article{
		{Slug: "seawalls-buy-time-not-safety", Title: "Seawalls Buy Time, Not Safety",
			Dek:      "Coastal defences work, which is exactly why they are dangerous.",
			Body:     []string{"The engineering is not in doubt.", "Development follows protection."},
			Category: "climate", Author: "noor-haddad", PublishedAt: at("2026-09-20T08:00:00Z"),
			ReadMinutes: 11, Tags: []string{"adaptation", "analysis"}, Views: 21030},
		{Slug: "the-grid-is-the-hard-part", Title: "The Grid Is the Hard Part",
			Dek:      "Generation got cheap. Transmission did not.",
			Body:     []string{"The cost curves outran every projection.", "Moving the electricity is another matter."},
			Category: "climate", Author: "noor-haddad", PublishedAt: at("2026-06-13T08:00:00Z"),
			ReadMinutes: 10, Tags: []string{"energy", "grid", "analysis"}, Views: 18760},
		{Slug: "the-index-that-ate-the-database", Title: "The Index That Ate the Database",
			Dek:      "A single well-meaning index turned a fast query into an outage.",
			Body:     []string{"The change was two lines.", "Nobody had modelled the write path."},
			Category: "technology", Author: "dilek-arslan", PublishedAt: at("2026-08-29T08:00:00Z"),
			ReadMinutes: 11, Tags: []string{"databases", "analysis"}, Views: 24310},
		{Slug: "what-a-cache-key-actually-costs", Title: "What a Cache Key Actually Costs",
			Dek:      "Every discriminator you add is a hit rate you give away.",
			Body:     []string{"Caching looks like a storage problem.", "The honest default is to include everything."},
			Category: "technology", Author: "dilek-arslan", PublishedAt: at("2026-09-11T08:00:00Z"),
			ReadMinutes: 7, Tags: []string{"caching"}, Views: 12980},
		{Slug: "the-standard-that-shipped-too-early", Title: "The Standard That Shipped Too Early",
			Dek:      "A specification finalised before anyone had implemented it twice.",
			Body:     []string{"Standards bodies have a rule of thumb.", "The first implementation revealed an ambiguity."},
			Category: "technology", Author: "dilek-arslan", PublishedAt: at("2026-04-16T08:00:00Z"),
			ReadMinutes: 8, Tags: []string{"standards"}, Views: 8130},
		{Slug: "archives-are-a-budget-line", Title: "Archives Are a Budget Line",
			Dek:      "What a culture preserves is decided by whoever signs off on storage.",
			Body:     []string{"Preservation is a procurement decision.", "The losses are rarely dramatic."},
			Category: "culture", Author: "helena-strand", PublishedAt: at("2026-09-08T08:00:00Z"),
			ReadMinutes: 8, Tags: []string{"archives"}, Views: 7310},
	}
	categories := []Category{
		{Slug: "technology", Name: "Technology", Description: "Systems and the people who maintain them."},
		{Slug: "climate", Name: "Climate", Description: "The measurements and the policy."},
		{Slug: "culture", Name: "Culture", Description: "What we make and what we keep."},
	}
	authors := []Author{
		{Slug: "noor-haddad", Name: "Noor Haddad", Role: "Climate reporter", Bio: "Reports on adaptation."},
		{Slug: "dilek-arslan", Name: "Dilek Arslan", Role: "Technology correspondent", Bio: "Covers infrastructure."},
		{Slug: "helena-strand", Name: "Helena Strand", Role: "Culture critic", Bio: "Writes about archives."},
	}
	return articles, categories, authors
}

// backend is a controllable stand-in for the newsroom API. Every test that needs a
// particular failure builds one of these rather than running the real API with its
// chaos knob: that schedule is shared across every endpoint, so "the sidebar is
// down but the article is fine" cannot be expressed with it, and the client's retry
// masks an intermittent schedule entirely.
type backend struct {
	articles   []Article
	categories []Category
	authors    []Author
	// failPrefixes lists path prefixes that answer 503 instead of data.
	failPrefixes []string
	// failFirst, when above zero, answers 503 to that many requests and then
	// serves normally. It exists to fail one fragment's lookup without failing the
	// next fragment's: the client retries once, so a lookup only fails outright
	// when two consecutive attempts do.
	failFirst atomic.Int64
	// hits counts every request, so a test can prove a render happened again
	// rather than being served from the page cache.
	hits atomic.Int64

	// inFlight and peak record how many requests the site had open at once. It is
	// how a test sees that fragments fetch concurrently without timing anything:
	// a peak above one is overlap, whatever the machine was doing.
	inFlight atomic.Int64
	peak     atomic.Int64
	// hold, when non-nil, blocks every request until it is closed or the caller
	// gives up, so the peak is the number of requests that were genuinely
	// simultaneous rather than a race between fast handlers.
	hold chan struct{}
}

func newBackend(t *testing.T, failPrefixes ...string) (*backend, *httptest.Server) {
	t.Helper()
	articles, categories, authors := testCorpus()
	b := &backend{articles: articles, categories: categories, authors: authors, failPrefixes: failPrefixes}
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	return b, srv
}

func (b *backend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.hits.Add(1)

	now := b.inFlight.Add(1)
	for {
		peak := b.peak.Load()
		if now <= peak || b.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	defer b.inFlight.Add(-1)
	if b.hold != nil {
		select {
		case <-b.hold:
		case <-r.Context().Done():
			// The caller gave up. Returning here is what makes inFlight mean
			// "requests the site is actually waiting on": a handler that stayed
			// blocked after its fragment timed out would keep the count up and
			// make sequential fetches look concurrent.
			return
		}
	}

	if b.failFirst.Load() > 0 {
		b.failFirst.Add(-1)
		http.Error(w, "injected", http.StatusServiceUnavailable)
		return
	}
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
		enc.Encode(b.categories)
	case r.URL.Path == "/v1/popular":
		ranked := append([]Article(nil), b.articles...)
		sort.Slice(ranked, func(i, j int) bool { return ranked[i].Views > ranked[j].Views })
		enc.Encode(ranked)
	case r.URL.Path == "/v1/articles":
		enc.Encode(b.list(r.URL.Query()))
	case strings.HasPrefix(r.URL.Path, "/v1/articles/"):
		slug := strings.TrimPrefix(r.URL.Path, "/v1/articles/")
		for _, art := range b.articles {
			if art.Slug == slug {
				enc.Encode(art)
				return
			}
		}
		http.Error(w, "not found", http.StatusNotFound)
	case strings.HasPrefix(r.URL.Path, "/v1/categories/"):
		slug := strings.TrimPrefix(r.URL.Path, "/v1/categories/")
		for _, cat := range b.categories {
			if cat.Slug == slug {
				enc.Encode(cat)
				return
			}
		}
		http.Error(w, "not found", http.StatusNotFound)
	case strings.HasPrefix(r.URL.Path, "/v1/authors/"):
		slug := strings.TrimPrefix(r.URL.Path, "/v1/authors/")
		for _, a := range b.authors {
			if a.Slug == slug {
				enc.Encode(a)
				return
			}
		}
		http.Error(w, "not found", http.StatusNotFound)
	default:
		http.Error(w, "no route", http.StatusNotFound)
	}
}

// testPerPage is small on purpose. The double has to paginate for the pager to
// render at all, and a pager that never renders is a pager whose links are never
// checked — which is how a next link that dropped the search term survived.
const testPerPage = 2

// list applies the same filters the real API does, newest first, and paginates. It
// is deliberately simple-minded: this double exists to feed the site, not to be a
// second implementation worth testing.
func (b *backend) list(values url.Values) Listing {
	matched := make([]Article, 0, len(b.articles))
	for _, art := range b.articles {
		if c := values.Get("category"); c != "" && art.Category != c {
			continue
		}
		if a := values.Get("author"); a != "" && art.Author != a {
			continue
		}
		if q := values.Get("q"); q != "" && !strings.Contains(strings.ToLower(art.Title+" "+art.Dek+" "+strings.Join(art.Tags, " ")), strings.ToLower(q)) {
			continue
		}
		matched = append(matched, art)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].PublishedAt.After(matched[j].PublishedAt) })

	page := 1
	if n, err := strconv.Atoi(values.Get("page")); err == nil && n > 0 {
		page = n
	}
	perPage := testPerPage
	if n, err := strconv.Atoi(values.Get("per_page")); err == nil && n > 0 {
		perPage = n
	}

	total := len(matched)
	start := min((page-1)*perPage, total)
	end := min(start+perPage, total)

	return Listing{
		Items:      matched[start:end],
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: (total + perPage - 1) / perPage,
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

func request(t *testing.T, h http.Handler, target string) (*httptest.ResponseRecorder, string) {
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
		{"/newsletter", 200, "text/html"},
		{"/tr/bulten", 200, "text/html"},
		{"/category/no-such-category", 404, "text/html"},
		{"/nothing/here/at-all", 404, "text/html"},
	} {
		rec, _ := request(t, site, tc.target)
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

	if rec, _ := request(t, site, "/kategori/technology"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /kategori/technology = %d, want 404", rec.Code)
	}
	if rec, _ := request(t, site, "/tr/category/technology"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /tr/category/technology = %d, want 404", rec.Code)
	}
}

func TestSite_TurkishPageLinksTurkishURLs(t *testing.T) {
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	_, body := request(t, site, "/tr/")
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

	if rec, _ := request(t, site, "/2026/09/seawalls-buy-time-not-safety"); rec.Code != http.StatusOK {
		t.Fatalf("the correct date = %d, want 200", rec.Code)
	}
	rec, body := request(t, site, "/2001/01/seawalls-buy-time-not-safety")
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

	rec, body := request(t, site, "/2026/09/seawalls-buy-time-not-safety")
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

	rec, body := request(t, site, "/")
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
	if rec, _ := request(t, site, target); rec.Code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", rec.Code)
	}
	after := b.hits.Load()

	if rec, _ := request(t, site, target); rec.Code != http.StatusOK {
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
	request(t, site, target)
	after := b.hits.Load()
	request(t, site, target)

	if b.hits.Load() != after {
		t.Errorf("backend hits went %d -> %d; a healthy render must be served from the cache on the second request", after, b.hits.Load())
	}
}

func TestSite_SearchIsNeverCached(t *testing.T) {
	// The page's cache key includes the raw query string, so caching it would mint
	// an entry per distinct "?q=" and let a crawler evict everything else.
	b, api := newBackend(t)
	site := newTestSite(t, api.URL)

	request(t, site, "/search?q=grid")
	after := b.hits.Load()
	request(t, site, "/search?q=grid")

	if b.hits.Load() == after {
		t.Error("the second identical search reached no backend, so it was cached")
	}
}

func TestSite_MissingArticleIs404NotAnError(t *testing.T) {
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	rec, _ := request(t, site, "/2026/09/no-such-piece")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (the client must keep ErrNotFound apart from an outage)", rec.Code)
	}
}

func TestSite_BackendOutageOnARequiredFragmentIs500(t *testing.T) {
	// The other half of the same distinction: an unreachable backend must not be
	// reported as a missing article, or a crawler concludes the archive was deleted.
	_, api := newBackend(t, "/v1/articles")
	site := newTestSite(t, api.URL)

	rec, body := request(t, site, "/2026/09/seawalls-buy-time-not-safety")
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

	_, body := request(t, site, "/rss.xml")

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

	_, body := request(t, site, "/sitemap.xml")

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

	rec, body := request(t, site, "/healthz")
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

	request(t, handler, "/")
	cached := b.hits.Load()
	request(t, handler, "/")
	if b.hits.Load() != cached {
		t.Fatalf("the second request was not served from the cache")
	}

	if err := app.InvalidateTags(t.Context(), "articles"); err != nil {
		t.Fatalf("InvalidateTags() error = %v", err)
	}

	request(t, handler, "/")
	if b.hits.Load() == cached {
		t.Error("the request after invalidation was still served from the cache")
	}
}

// compile-time check that the site builder returns the framework's App, so a
// refactor cannot quietly change what main receives.
var _ func(config) (*collage.App, *Client, error) = newSite

func TestSite_EveryPageHasItsOwnTitle(t *testing.T) {
	// The reason the head is a slot. The layout's data handler runs before the
	// content slot renders — fragments render depth-first and a slot is filled
	// during the parent's template execution — so a <title> built from the layout's
	// own data is the site name on every page, which is what the simpler
	// arrangement produces.
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	// Counted, not merely contained: a layout writing its own <title> beside a
	// hoisted one produces two, and every assertion built on Contains passes.
	for _, tc := range []struct{ target, want string }{
		{"/", "<title>Latest — The Wire</title>"},
		{"/tr/", "<title>En yeni — The Wire</title>"},
		{"/category/technology", "<title>Technology — The Wire</title>"},
		{"/author/noor-haddad", "<title>Noor Haddad — The Wire</title>"},
		{"/2026/09/seawalls-buy-time-not-safety", "<title>Seawalls Buy Time, Not Safety — The Wire</title>"},
		{"/search?q=grid", "<title>grid — Search — The Wire</title>"},
		{"/nothing/here", "<title>Not found — The Wire</title>"},
	} {
		_, body := request(t, site, tc.target)
		if !strings.Contains(body, tc.want) {
			t.Errorf("GET %s: title missing %q", tc.target, tc.want)
		}
		if n := strings.Count(body, "<title>"); n != 1 {
			t.Errorf("GET %s has %d <title> elements, want 1", tc.target, n)
		}
		if n := strings.Count(body, `<meta name="description"`); n != 1 {
			t.Errorf("GET %s has %d description meta elements, want 1", tc.target, n)
		}
	}
}

func TestSite_ArticleIsFetchedOncePerRender(t *testing.T) {
	// The head fragment and the content fragment both need the article. Without the
	// SharedData memoisation each uncached page view costs two identical requests.
	b, api := newBackend(t)
	site := newTestSite(t, api.URL)

	before := b.hits.Load()
	if rec, _ := request(t, site, "/2026/09/seawalls-buy-time-not-safety"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	paths := b.hits.Load() - before

	// nav + popular + the article itself.
	if paths != 3 {
		t.Errorf("one article render made %d backend requests, want 3 (nav, popular, article); the article is being fetched twice", paths)
	}
}

// TestSite_AFailedPageKeepsTheLayoutsTitle is what the hoist default is for.
//
// A content fragment hoists its own title from its data handler. A handler that
// fails never gets there — so what the reader ends up with, on the error page the
// framework substitutes, is the layout's default. Without one the page would be
// titleless, which in a browser's history is indistinguishable from any other page
// on the site.
func TestSite_AFailedPageKeepsTheLayoutsTitle(t *testing.T) {
	_, api := newBackend(t, "/v1/categories/")
	site := newTestSite(t, api.URL)

	rec, body := request(t, site, "/category/technology")
	if rec.Code == http.StatusOK {
		t.Fatalf("status = %d, want a failure: the category lookup is refused", rec.Code)
	}
	if !strings.Contains(body, "<title>") {
		t.Errorf("the error page has no title at all:\n%s", body)
	}
	if !strings.Contains(body, "The Wire</title>") {
		t.Errorf("the title does not name the site:\n%s", body)
	}
}
func TestSite_SearchPagerCarriesTheQuery(t *testing.T) {
	// A next link built from the listing path alone sends the reader to page two of
	// nothing: the term lives in the query string, and dropping it turns "results 3
	// to 4 for analysis" into "results 3 to 4 for everything".
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	_, body := request(t, site, "/search?q=analysis")

	next := nextLink(t, body)
	if !strings.Contains(next, "q=analysis") {
		t.Errorf("next link = %q, want the search term carried over", next)
	}
	if !strings.Contains(next, "page=2") {
		t.Errorf("next link = %q, want page=2", next)
	}
	if !strings.HasPrefix(next, "/search?") {
		t.Errorf("next link = %q, want it to stay on /search", next)
	}
}

func TestSite_SearchPageTwoStillSearches(t *testing.T) {
	// Following the link has to land on the same search. This is the assertion the
	// URL-shape test above cannot make on its own: it checks the link works, not
	// just that it looks right.
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	_, first := request(t, site, "/search?q=analysis")
	next := nextLink(t, first)

	rec, second := request(t, site, next)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", next, rec.Code)
	}

	// Scoped to <main>: the "most read" sidebar lists the whole corpus on every
	// page, so a check against the full body would be answered by the furniture
	// rather than by the results.
	results := mainContent(t, second)
	if !strings.Contains(results, "3 results for") {
		t.Errorf("page two does not report the search's own total; the term was lost")
	}
	if !strings.Contains(results, "The Grid Is the Hard Part") {
		t.Error("page two does not list the third match")
	}
	if strings.Contains(results, "Archives Are a Budget Line") {
		t.Error("page two lists an article the search does not match, so it paginated the whole corpus")
	}
}

func TestSite_TurkishSearchPagerCarriesTheQuery(t *testing.T) {
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	_, body := request(t, site, "/tr/arama?q=analysis")

	next := nextLink(t, body)
	if !strings.HasPrefix(next, "/tr/arama?") || !strings.Contains(next, "q=analysis") {
		t.Errorf("next link = %q, want the Turkish search path with the term carried over", next)
	}
}

func TestSite_ListingPagerLinksAreCanonical(t *testing.T) {
	// Page one carries no "page" parameter, so the front page has one URL rather
	// than two that a crawler indexes separately and the cache stores twice.
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	_, second := request(t, site, "/?page=2")

	prev := linkWithRel(t, second, "prev")
	if prev != "/" {
		t.Errorf("prev link from page 2 = %q, want %q", prev, "/")
	}
	if next := linkWithRel(t, second, "next"); next != "/?page=3" {
		t.Errorf("next link from page 2 = %q, want %q", next, "/?page=3")
	}
}

func TestSite_CategoryPagerStaysInTheCategory(t *testing.T) {
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	_, body := request(t, site, "/category/technology")

	next := nextLink(t, body)
	if next != "/category/technology?page=2" {
		t.Errorf("next link = %q, want %q", next, "/category/technology?page=2")
	}

	rec, second := request(t, site, next)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", next, rec.Code)
	}
	if results := mainContent(t, second); strings.Contains(results, "Seawalls Buy Time") {
		t.Error("page two of a section lists an article from another section")
	}
}

// mainContent returns just the page's <main> element. The layout's sidebar lists
// the whole corpus on every page, so an assertion against the whole document can be
// satisfied by the furniture rather than by what the page is actually about.
func mainContent(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `<main id="main">`)
	end := strings.Index(body, "</main>")
	if start < 0 || end < start {
		t.Fatal("no <main> element in the response")
	}
	return body[start:end]
}

var relLink = regexp.MustCompile(`<a rel="(prev|next)" href="([^"]*)"`)

// linkWithRel returns the href of the pager link with the given rel, unescaping the
// HTML entities the template engine writes into attributes — "&amp;" between query
// parameters would otherwise make every assertion about a two-parameter URL fail for
// the wrong reason.
func linkWithRel(t *testing.T, body, rel string) string {
	t.Helper()
	for _, m := range relLink.FindAllStringSubmatch(body, -1) {
		if m[1] == rel {
			return html.UnescapeString(m[2])
		}
	}
	t.Fatalf("no pager link with rel=%q in the response", rel)
	return ""
}

func nextLink(t *testing.T, body string) string {
	t.Helper()
	return linkWithRel(t, body, "next")
}

func TestSite_SearchCountAgreesInNumber(t *testing.T) {
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	_, one := request(t, site, "/search?q=databases")
	if !strings.Contains(mainContent(t, one), "1 result for") {
		t.Error(`a single match reads "1 results"`)
	}
	_, many := request(t, site, "/search?q=analysis")
	if !strings.Contains(mainContent(t, many), "3 results for") {
		t.Error("a multiple match does not report the plural")
	}
}

func TestSite_TrackingParametersDoNotMintCacheEntries(t *testing.T) {
	// The listing pages declare that only "page" discriminates. Without that, a
	// newsletter link or a crawler appending "?utm_source=..." caches a separate
	// copy of the archive per variant, evicting real pages from a bounded cache
	// without ever asking for a distinct one.
	b, api := newBackend(t)
	site := newTestSite(t, api.URL)

	request(t, site, "/")
	cached := b.hits.Load()

	request(t, site, "/?utm_source=newsletter&fbclid=abc123")
	if b.hits.Load() != cached {
		t.Error("a tracking parameter reached the backend, so it minted its own cache entry")
	}

	request(t, site, "/?page=2")
	if b.hits.Load() == cached {
		t.Error("?page=2 was served from the front page's cache entry; the page number must still discriminate")
	}
}

func TestSite_ArticlesIgnoreTheQueryEntirely(t *testing.T) {
	b, api := newBackend(t)
	site := newTestSite(t, api.URL)

	target := "/2026/09/seawalls-buy-time-not-safety"
	request(t, site, target)
	cached := b.hits.Load()

	request(t, site, target+"?anything=at-all")
	if b.hits.Load() != cached {
		t.Error("a query parameter re-rendered an article that reads none")
	}
}

// The stylesheet is linked by its content-addressed name and served as immutable.
//
// This is the whole reason to fingerprint: a name derived from the bytes cannot
// describe anything else, so the browser is told never to ask again. A plain name
// can only ever carry a short lifetime, because the file behind it may change.
func TestSite_StylesheetIsLinkedByContentAndServedForever(t *testing.T) {
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	_, body := request(t, site, "/")
	link := regexp.MustCompile(`href="(/static/magazine\.[0-9a-f]{16}\.css)"`).FindStringSubmatch(body)
	if link == nil {
		t.Fatalf("the home page does not link a content-addressed stylesheet:\n%s",
			firstLines(body, 20))
	}

	rec, css := request(t, site, link[1])
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", link[1], rec.Code)
	}
	if len(css) == 0 {
		t.Error("the stylesheet served no bytes")
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable", cc)
	}

	// The same URL with a hash that is not the file's must not be served at all.
	// It would otherwise be cached for a year by everything between the site and
	// the reader.
	wrong, _ := request(t, site, "/static/magazine.0123456789abcdef.css")
	if wrong.Code != http.StatusNotFound {
		t.Errorf("GET a wrong fingerprint = %d, want 404", wrong.Code)
	}
}

// firstLines returns at most n lines of s, for a failure message that shows enough
// of a page to see what went wrong without printing the whole document.
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// The fragments of one page fetch at the same time, not one after another.
//
// A home page is a navigation bar, a list of articles and a popular sidebar. None of
// them needs anything from the others, and rendering them in sequence means the
// reader waits for the sum of three round trips rather than the longest one.
//
// Measured as the peak number of requests the newsroom had open at once, so the
// assertion says nothing about how fast any machine is.
func TestSite_FragmentsFetchConcurrently(t *testing.T) {
	b, api := newBackend(t)

	// Held open until every request that is going to arrive has arrived, so the
	// peak is what was genuinely simultaneous. Three is what the home page asks
	// for: categories, articles, popular.
	b.hold = make(chan struct{})
	go func() {
		for b.inFlight.Load() < 3 {
			runtime.Gosched()
		}
		close(b.hold)
	}()

	site := newTestSite(t, api.URL)
	rec, _ := request(t, site, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}

	t.Logf("peak concurrent upstream requests = %d", b.peak.Load())
	if peak := b.peak.Load(); peak < 2 {
		t.Errorf("peak concurrent upstream requests = %d, want at least 2: the fragments are still waiting for each other", peak)
	}
}

// ---------------------------------------------------------------------------
// The newsletter form
// ---------------------------------------------------------------------------

// token reads the forgery token out of a rendered page, the way a browser would read
// it out of the form.
func token(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no forgery token in the page:\n%s", firstLines(body, 30))
	}
	return m[1]
}

func submit(t *testing.T, site http.Handler, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/newsletter", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	site.ServeHTTP(rec, req)
	return rec
}

// The whole form flow, as a browser walks it: read the page, submit what it carried,
// follow the redirect.
func TestSite_NewsletterAcceptsAValidAddress(t *testing.T) {
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	page, body := request(t, site, "/newsletter")
	csrfToken := token(t, body)

	rec := submit(t, site, url.Values{"_csrf": {csrfToken}, "email": {"reader@example.com"}},
		(&http.Response{Header: page.Header()}).Cookies())

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303: a form post must redirect, or a reload submits it again", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "subscribed=1") {
		t.Errorf("Location = %q, want the destination to say it worked", loc)
	}
}

// A refused address comes back on the page it was typed on, with the reason and with
// what was typed — no session, no flash storage, no state in a query string.
func TestSite_NewsletterRefusesAnInvalidAddress(t *testing.T) {
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	page, body := request(t, site, "/newsletter")
	csrfToken := token(t, body)

	rec := submit(t, site, url.Values{"_csrf": {csrfToken}, "email": {"not-an-address"}},
		(&http.Response{Header: page.Header()}).Cookies())

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "does not look like an email address") {
		t.Error("the page does not say why the submission was refused")
	}
	if !strings.Contains(rec.Body.String(), `value="not-an-address"`) {
		t.Error("what the reader typed was not echoed back; a refused form is not also an empty one")
	}
}

// Without the token the submission never reaches the handler. This is the test that
// would fail if the protection were quietly turned off.
func TestSite_NewsletterRefusesASubmissionWithNoToken(t *testing.T) {
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	rec := submit(t, site, url.Values{"email": {"reader@example.com"}}, nil)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// Two readers are handed two different tokens.
//
// The property that the body behind those tokens is cacheable belongs to the
// framework and is tested there (internal/httpx). What this checks is the part a
// site can get wrong on its own: that two readers are never given one token, which
// would be a token anybody obtains by visiting.
func TestSite_TwoReadersGetTwoTokens(t *testing.T) {
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	_, first := request(t, site, "/newsletter")
	_, second := request(t, site, "/newsletter")

	if token(t, first) == token(t, second) {
		t.Error("two readers were handed one token")
	}
}

// The results fragment answers on its own, and what comes back is the list rather
// than the page around it.
func TestSite_SearchResultsAnswerOnTheirOwn(t *testing.T) {
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	whole, page := request(t, site, "/search?q=grid")
	part, fragment := request(t, site, "/search/results?q=grid")

	if whole.Code != http.StatusOK || part.Code != http.StatusOK {
		t.Fatalf("statuses = %d, %d, want 200", whole.Code, part.Code)
	}
	if len(fragment) >= len(page) {
		t.Errorf("the fragment is %d bytes and the page is %d: the fragment is not smaller", len(fragment), len(page))
	}
	if strings.Contains(fragment, "<html") {
		t.Error("the fragment came back wrapped in the layout")
	}
	if !strings.Contains(fragment, "cards") {
		t.Errorf("the fragment does not contain the result list:\n%s", firstLines(fragment, 10))
	}
}

// A method no route answers is a 405 naming what the URL does accept, not a 404 and
// not a silently rendered page.
func TestSite_UnsupportedMethodsAreRefused(t *testing.T) {
	_, api := newBackend(t)
	site := newTestSite(t, api.URL)

	// The newsletter page answers POST, because its form does; the home page does
	// not, and a DELETE reaches neither.
	for path, want := range map[string][]string{
		"/":           {"GET", "HEAD", "OPTIONS"},
		"/newsletter": {"GET", "HEAD", "OPTIONS", "POST"},
	} {
		rec := httptest.NewRecorder()
		site.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, path, nil))

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("DELETE %s = %d, want 405", path, rec.Code)
		}
		allow := rec.Header().Get("Allow")
		for _, method := range want {
			if !strings.Contains(allow, method) {
				t.Errorf("DELETE %s Allow = %q, want it to contain %s", path, allow, method)
			}
		}
	}
}
