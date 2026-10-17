package collage_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// headline is what a newsSite's fragments render.
type headline struct{ Text string }

// newsSite is one page, "/", with a required headline and an optional aside, each
// read from the site's own fields through plain Load, not Cached: the page cache
// is what is under test, so no data cache stands between a render and the
// upstream. The page carries the tag "news".
type newsSite struct {
	app *collage.App
	mu  sync.Mutex
	// text and aside are what the two fragments render.
	text, aside string
	// textErr and asideErr, when set, are what the fragments' data returns.
	textErr, asideErr error
	// renders counts headline data calls, failed ones included.
	renders int
}

type newsOptions struct {
	devMode bool
	cache   collage.CacheConfig
	ttl     time.Duration
	// dynamic makes the page uncacheable.
	dynamic bool
	// plugins and logger, when set, go into the Config.
	plugins []collage.Plugin
	logger  *slog.Logger
	// warm leaves warming on.
	warm bool
}

func newNewsSite(t *testing.T, opts newsOptions) *newsSite {
	t.Helper()
	if opts.cache == (collage.CacheConfig{}) {
		opts.cache = collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour}
	}
	if opts.ttl == 0 {
		opts.ttl = time.Hour
	}
	// These are what a reader sees of an invalidated page nothing has re-made
	// yet: the warmer, which would race the reader to it, is off unless asked
	// for. See warm_test.go.
	if !opts.warm {
		opts.cache.WarmLimit = -1
	}
	site := &newsSite{text: "Old headline", aside: "Old aside"}
	app, err := collage.New(&collage.Config{
		DevMode: opts.devMode,
		Server:  collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/page.html":  {Data: []byte(`<main>{{slot "body"}}{{slot "aside"}}</main>`)},
			"t/body.html":  {Data: []byte(`<h1>{{.Text}}</h1>`)},
			"t/aside.html": {Data: []byte(`<aside>{{.Text}}</aside>`)},
		}, Root: "t"},
		Cache:   opts.cache,
		Plugins: opts.plugins,
		Logger:  opts.logger,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := collage.NewFragment("body", "body.html").WithData(collage.Load(
		func(context.Context, *collage.RenderContext) (headline, error) {
			site.mu.Lock()
			defer site.mu.Unlock()
			site.renders++
			if site.textErr != nil {
				return headline{}, site.textErr
			}
			return headline{Text: site.text}, nil
		})).Required().Build()
	aside := collage.NewFragment("aside", "aside.html").WithData(collage.Load(
		func(context.Context, *collage.RenderContext) (headline, error) {
			site.mu.Lock()
			defer site.mu.Unlock()
			if site.asideErr != nil {
				return headline{}, site.asideErr
			}
			return headline{Text: site.aside}, nil
		})).Build()
	content := collage.NewFragment("page", "page.html").
		WithSlot("body", collage.SlotRequired, collage.SlotSingle).WithSlotFragment("body", body).
		WithSlot("aside", collage.SlotSingle).WithSlotFragment("aside", aside).Build()
	builder := collage.NewPage("home").WithContent(content).WithPath("en", "/").WithDependencyTags("news")
	if opts.dynamic {
		builder = builder.Dynamic()
	} else {
		builder = builder.Incremental(opts.ttl)
	}
	if err := app.RegisterPage(builder.Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	site.app = app
	return site
}

// set changes the site's upstream under its lock.
func (s *newsSite) set(change func(*newsSite)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(s)
}

func (s *newsSite) do(method string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.app.Handler().ServeHTTP(rec, httptest.NewRequest(method, "/", nil))
	return rec
}

func (s *newsSite) get() *httptest.ResponseRecorder { return s.do(http.MethodGet) }

const (
	oldNews = "<main><h1>Old headline</h1><aside>Old aside</aside></main>"
	newNews = "<main><h1>New headline</h1><aside>Old aside</aside></main>"
)

// expect checks a response's status, body and cache status header.
func expect(t *testing.T, what string, rec *httptest.ResponseRecorder, code int, body, status string) {
	t.Helper()
	if rec.Code != code || rec.Body.String() != body {
		t.Errorf("%s = %d %q, want %d %q", what, rec.Code, rec.Body.String(), code, body)
	}
	if got := rec.Header().Get(collage.CacheStatusHeader); got != status {
		t.Errorf("%s: %s = %q, want %q", what, collage.CacheStatusHeader, got, status)
	}
}

// The point of v0.63: invalidated, the upstream down, the old page whole, for
// either built-in store.
func TestStalePages_ServedWhileTheUpstreamIsDown(t *testing.T) {
	for _, typ := range []collage.CacheType{"memory", "disk"} {
		t.Run(string(typ), func(t *testing.T) {
			cfg := collage.CacheConfig{Enabled: true, Type: typ, DefaultTTL: time.Hour}
			if typ == "disk" {
				cfg = diskCache(t.TempDir(), "build-1")
			}
			site := newNewsSite(t, newsOptions{cache: cfg})
			expect(t, "warm", site.get(), http.StatusOK, oldNews, "MISS")
			expect(t, "warm again", site.get(), http.StatusOK, oldNews, "HIT")

			site.set(func(s *newsSite) { s.textErr = errors.New("cms down") })
			if err := site.app.InvalidateTags(context.Background(), "news"); err != nil {
				t.Fatal(err)
			}
			rec := site.get()
			expect(t, "invalidated, upstream down", rec, http.StatusOK, oldNews, "STALE")
			if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control on a stale page = %q, want no-store", cc)
			}

			site.set(func(s *newsSite) { s.textErr = nil; s.text = "New headline" })
			expect(t, "upstream back", site.get(), http.StatusOK, newNews, "MISS")
			expect(t, "after it", site.get(), http.StatusOK, newNews, "HIT")
		})
	}
}

// An optional part failing is a degraded render: the reader gets the old page
// whole, not the new one with a hole where the aside was.
func TestStalePages_ADegradedRenderServesTheStalePage(t *testing.T) {
	site := newNewsSite(t, newsOptions{})
	site.get()
	site.set(func(s *newsSite) { s.text = "New headline"; s.asideErr = errors.New("aside down") })
	if err := site.app.InvalidateTags(context.Background(), "news"); err != nil {
		t.Fatal(err)
	}
	rec := site.get()
	expect(t, "degraded", rec, http.StatusOK, oldNews, "STALE")
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	// Not stored either: the next request renders again.
	site.set(func(s *newsSite) { s.asideErr = nil })
	expect(t, "recovered", site.get(), http.StatusOK, newNews, "MISS")
}

// Content removed upstream leaves the site: a not found never falls back, and the
// stale copy goes with it, so a later failure is not answered with it either.
func TestStalePages_NotFoundDropsTheEntry(t *testing.T) {
	site := newNewsSite(t, newsOptions{})
	site.get()
	site.set(func(s *newsSite) { s.textErr = fmt.Errorf("gone: %w", collage.ErrNotFound) })
	if err := site.app.InvalidateTags(context.Background(), "news"); err != nil {
		t.Fatal(err)
	}
	if rec := site.get(); rec.Code != http.StatusNotFound {
		t.Fatalf("not found upstream = %d %q, want 404", rec.Code, rec.Body.String())
	} else if got := rec.Header().Get(collage.CacheStatusHeader); got != "" {
		t.Errorf("a 404 carries %s = %q, want none", collage.CacheStatusHeader, got)
	}
	// A plain failure now has no stale page to fall back to.
	site.set(func(s *newsSite) { s.textErr = errors.New("cms down") })
	if rec := site.get(); rec.Code != http.StatusInternalServerError {
		t.Errorf("after the drop, a failure = %d %q, want 500: the stale page was kept", rec.Code, rec.Body.String())
	}
}

// A page past its TTL is stale, not gone.
func TestStalePages_AnExpiredPageIsStale(t *testing.T) {
	site := newNewsSite(t, newsOptions{ttl: 50 * time.Millisecond})
	site.get()
	time.Sleep(150 * time.Millisecond)
	site.set(func(s *newsSite) { s.textErr = errors.New("cms down") })
	expect(t, "expired, upstream down", site.get(), http.StatusOK, oldNews, "STALE")
}

// mapStore is a Cache.Store of the application's own: Cache alone, neither
// TaggedCache nor StaleCache, written from scratch so nothing of the built-in
// stores is promoted into it.
type mapStore struct {
	mu      sync.Mutex
	entries map[string][]byte
}

func (s *mapStore) Get(_ context.Context, key string) ([]byte, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content, ok := s.entries[key]
	return content, collage.ETag(content), ok
}

func (s *mapStore) Set(_ context.Context, key string, content []byte, _ time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = append([]byte(nil), content...)
	return collage.ETag(content), nil
}

func (s *mapStore) InvalidateKey(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
	return nil
}

func (s *mapStore) Clear(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.entries)
	return nil
}

func (s *mapStore) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// A store that keeps nothing stale behaves as before v0.63: invalidation drops
// the page, and a failing render is an error page, never STALE.
func TestStalePages_ACustomStoreKeepsDropping(t *testing.T) {
	store := &mapStore{entries: map[string][]byte{}}
	site := newNewsSite(t, newsOptions{cache: collage.CacheConfig{Enabled: true, Store: store, DefaultTTL: time.Hour}})
	expect(t, "warm", site.get(), http.StatusOK, oldNews, "MISS")
	expect(t, "hit", site.get(), http.StatusOK, oldNews, "HIT")
	site.set(func(s *newsSite) { s.textErr = errors.New("cms down") })
	if err := site.app.InvalidateTags(context.Background(), "news"); err != nil {
		t.Fatal(err)
	}
	if store.size() != 0 {
		t.Errorf("the custom store holds %d entries after the invalidation, want none", store.size())
	}
	rec := site.get()
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("custom store, upstream down = %d %q, want 500", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(collage.CacheStatusHeader); got != "" {
		t.Errorf("%s = %q on the error page, want none", collage.CacheStatusHeader, got)
	}
}

// PurgeTags is the hard delete for pages as for data: nothing stale is left, so a
// failing render is an error page, as before v0.62.
func TestStalePages_PurgeLeavesNothingStale(t *testing.T) {
	site := newNewsSite(t, newsOptions{})
	site.get()
	site.set(func(s *newsSite) { s.textErr = errors.New("cms down") })
	if err := site.app.PurgeTags(context.Background(), "news"); err != nil {
		t.Fatal(err)
	}
	if rec := site.get(); rec.Code != http.StatusInternalServerError {
		t.Errorf("after PurgeTags, upstream down = %d %q, want 500", rec.Code, rec.Body.String())
	}
}

// A cached document falls back to its stale body as a page does, and a not found
// drops it.
func TestStalePages_Documents(t *testing.T) {
	site := newNewsSite(t, newsOptions{})
	var feedErr error
	feed := "old feed"
	doc := collage.NewDocument("feed", "text/plain").WithPath("en", "/feed.txt").Incremental(time.Hour).
		WithDependencyTags("news").
		WithHandler(func(context.Context, *collage.RenderContext) ([]byte, []string, error) {
			site.mu.Lock()
			defer site.mu.Unlock()
			return []byte(feed), nil, feedErr
		}).Build()
	if err := site.app.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	serve := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		site.app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/feed.txt", nil))
		return rec
	}
	expect(t, "warm", serve(), http.StatusOK, "old feed", "MISS")
	expect(t, "hit", serve(), http.StatusOK, "old feed", "HIT")

	site.set(func(*newsSite) { feedErr = errors.New("cms down"); feed = "" })
	if err := site.app.InvalidateTags(context.Background(), "news"); err != nil {
		t.Fatal(err)
	}
	rec := serve()
	expect(t, "invalidated, upstream down", rec, http.StatusOK, "old feed", "STALE")
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control on a stale document = %q, want no-store", cc)
	}

	site.set(func(*newsSite) { feedErr = fmt.Errorf("gone: %w", collage.ErrNotFound) })
	if rec := serve(); rec.Code != http.StatusNotFound {
		t.Fatalf("not found upstream = %d %q, want 404", rec.Code, rec.Body.String())
	}
	site.set(func(*newsSite) { feedErr = errors.New("cms down") })
	if rec := serve(); rec.Code != http.StatusInternalServerError {
		t.Errorf("after the drop, a failure = %d %q, want 500: the stale document was kept", rec.Code, rec.Body.String())
	}
}

// The header says MISS then HIT on a cached page, and nothing where the cache is
// not in play.
func TestStalePages_HitMissHeaders(t *testing.T) {
	site := newNewsSite(t, newsOptions{})
	expect(t, "first", site.get(), http.StatusOK, oldNews, "MISS")
	expect(t, "second", site.get(), http.StatusOK, oldNews, "HIT")
	head := site.do(http.MethodHead)
	if got := head.Header().Get(collage.CacheStatusHeader); got != "HIT" {
		t.Errorf("HEAD: %s = %q, want HIT", collage.CacheStatusHeader, got)
	}

	dev := newNewsSite(t, newsOptions{devMode: true})
	for i := range 2 {
		if got := dev.get().Header().Get(collage.CacheStatusHeader); got != "" {
			t.Errorf("development request %d: %s = %q, want none", i, collage.CacheStatusHeader, got)
		}
	}

	dynamic := newNewsSite(t, newsOptions{dynamic: true})
	if got := dynamic.get().Header().Get(collage.CacheStatusHeader); got != "" {
		t.Errorf("uncacheable page: %s = %q, want none", collage.CacheStatusHeader, got)
	}
}

// A document whose handler ran across an invalidation is served but not stored,
// as a page's render is: it may have read what the invalidation replaced.
func TestStalePages_ADocumentOverlappingAnInvalidationIsNotStored(t *testing.T) {
	site := newNewsSite(t, newsOptions{})
	var runs int
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	doc := collage.NewDocument("feed", "text/plain").WithPath("en", "/feed.txt").Incremental(time.Hour).
		WithDependencyTags("news").
		WithHandler(func(context.Context, *collage.RenderContext) ([]byte, []string, error) {
			site.mu.Lock()
			runs++
			first := runs == 1
			site.mu.Unlock()
			if first {
				started <- struct{}{}
				<-release
			}
			return []byte("feed"), nil, nil
		}).Build()
	if err := site.app.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	serve := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		site.app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/feed.txt", nil))
		return rec
	}
	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- serve() }()
	<-started
	if err := site.app.InvalidateTags(context.Background(), "news"); err != nil {
		t.Fatal(err)
	}
	close(release)
	expect(t, "overlapped", <-done, http.StatusOK, "feed", "MISS")
	expect(t, "the next request", serve(), http.StatusOK, "feed", "MISS")
	expect(t, "the one after", serve(), http.StatusOK, "feed", "HIT")
	site.mu.Lock()
	defer site.mu.Unlock()
	if runs != 2 {
		t.Errorf("handler runs = %d, want 2", runs)
	}
}

// A stale page hides an outage from readers, not from the operator: the failed
// render reaches the error hooks once, with a zero status because the reader got
// the stale page. A degraded fallback has no error to report; its failed fragment
// is reported as any degraded render's is, by the render engine's warning, and the
// error hooks hear nothing of it, stale or not.
func TestStalePages_TheHiddenFailureReachesTheErrorHook(t *testing.T) {
	events := &eventRecorder{}
	logs := &lockedBuffer{}
	site := newNewsSite(t, newsOptions{
		plugins: []collage.Plugin{events},
		logger:  slog.New(slog.NewTextHandler(logs, nil)),
	})
	site.get()
	const fragmentWarning = "fragment failed; the page is served without it"

	site.set(func(s *newsSite) { s.asideErr = errors.New("aside down") })
	if err := site.app.InvalidateTags(context.Background(), "news"); err != nil {
		t.Fatal(err)
	}
	expect(t, "degraded", site.get(), http.StatusOK, oldNews, "STALE")
	if got := len(events.at("/")); got != 0 {
		t.Errorf("error hook calls for a degraded fallback = %d, want 0", got)
	}
	if got := strings.Count(logs.String(), fragmentWarning); got != 1 {
		t.Errorf("fragment warnings = %d, want 1: the degraded render reports its fragment as always", got)
	}

	site.set(func(s *newsSite) { s.asideErr = nil; s.textErr = errors.New("cms down") })
	expect(t, "failed", site.get(), http.StatusOK, oldNews, "STALE")
	got := events.at("/")
	if len(got) != 1 {
		t.Fatalf("error hook calls for a failed render served stale = %d, want 1", len(got))
	}
	if got[0].Status != 0 || !strings.Contains(got[0].Err.Error(), "cms down") || got[0].Stage != collage.StageRender {
		t.Errorf("error event = status %d stage %q err %v, want 0, render, cms down", got[0].Status, got[0].Stage, got[0].Err)
	}
}
