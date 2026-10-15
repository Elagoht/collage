package collage_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

type author struct{ Name string }

// authorIDKey carries a post's author ID down to the author card it fills.
var authorIDKey = collage.NewKey[string]("author")

// authorKey is what Cached keeps one author under: authorKey.With(id).
var authorKey = collage.NewKey[author]("author")

// authorSite is thirty posts, twenty by A and ten by B, each showing an author
// card whose data handler fetches through Cached and counts what it fetched.
type authorSite struct {
	app   *collage.App
	mu    sync.Mutex
	calls map[string]int
	names map[string]string
	// failure, when set, is what the author fetch returns instead of a name.
	failure error
	// gate, when set, holds every author fetch until it is closed.
	gate chan struct{}
	// attempts counts author fetches, failed ones included.
	attempts atomic.Int32
}

func newAuthorSite(t *testing.T, devMode bool) *authorSite {
	t.Helper()
	return newAuthorSiteWith(t, devMode, collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour})
}

func newAuthorSiteWith(t *testing.T, devMode bool, cacheConfig collage.CacheConfig) *authorSite {
	t.Helper()
	site := &authorSite{calls: map[string]int{}, names: map[string]string{"A": "Ada", "B": "Bo"}}
	app, err := collage.New(&collage.Config{
		DevMode: devMode,
		Server:  collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/post.html":   {Data: []byte(`<article>{{slot "author"}}</article>`)},
			"t/author.html": {Data: []byte(`<p>{{.Name}}</p>`)},
		}, Root: "t"},
		Cache: cacheConfig,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	card := collage.NewFragment("author", "author.html").WithData(collage.LoadTagged(
		func(_ context.Context, rc *collage.RenderContext) (author, []string, error) {
			id, _ := authorIDKey.Get(rc)
			a, err := collage.Cached(rc, authorKey.With(id), time.Hour, []string{"author:" + id},
				func(context.Context) (author, error) {
					site.attempts.Add(1)
					site.mu.Lock()
					gate := site.gate
					site.mu.Unlock()
					if gate != nil {
						<-gate
					}
					site.mu.Lock()
					defer site.mu.Unlock()
					if site.failure != nil {
						return author{}, site.failure
					}
					site.calls[id]++
					return author{Name: site.names[id]}, nil
				})
			return a, nil, err
		})).
		Required().Build()
	for i := range 30 {
		id := "A"
		if i >= 20 {
			id = "B"
		}
		post := collage.NewFragment(fmt.Sprintf("post-%d", i), "post.html").WithData(collage.Effect(
			func(_ context.Context, rc *collage.RenderContext) error { authorIDKey.Set(rc, id); return nil })).
			WithSlot("author", collage.SlotRequired, collage.SlotSingle).WithSlotFragment("author", card).Build()
		page := collage.NewPage(fmt.Sprintf("p%d", i)).WithContent(post).
			WithPath("en", fmt.Sprintf("/p/%d", i)).Incremental(time.Hour).Build()
		if err := app.RegisterPage(page); err != nil {
			t.Fatalf("RegisterPage: %v", err)
		}
	}
	site.app = app
	return site
}

func (s *authorSite) fetched() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for k, v := range s.calls {
		out[k] = v
	}
	return out
}

// hold makes every author fetch wait until the returned gate is closed.
func (s *authorSite) hold() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gate = make(chan struct{})
	return s.gate
}

func (s *authorSite) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failure = err
}

func (s *authorSite) serve(t *testing.T, i int) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/p/%d", i), nil))
	return rec
}

// eventually re-requests page i until check passes, for what a background
// refresh changes; it fails after two seconds.
func (s *authorSite) eventually(t *testing.T, i int, what string, check func(*httptest.ResponseRecorder) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rec := s.serve(t, i)
		if check(rec) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("page %d never showed %s; last: %d %q", i, what, rec.Code, rec.Body.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *authorSite) get(t *testing.T, i int, preview bool) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/p/%d", i), nil)
	if preview {
		req.Header.Set("X-Preview", "1")
	}
	rec := httptest.NewRecorder()
	s.app.Handler().ServeHTTP(rec, req)
	return rec.Body.String()
}

// Thirty pages by two authors ask for each author once — exported or served.
func TestCached_SharesAcrossPages(t *testing.T) {
	site := newAuthorSite(t, false)
	if _, err := collage.Build(context.Background(), site.app, collage.BuildOptions{OutDir: t.TempDir(), Concurrency: 8}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := site.fetched(); got["A"] != 1 || got["B"] != 1 {
		t.Errorf("export fetched %v, want each author once", got)
	}

	served := newAuthorSite(t, false)
	for i := range 30 {
		served.get(t, i, false)
	}
	if got := served.fetched(); got["A"] != 1 || got["B"] != 1 {
		t.Errorf("serving fetched %v, want each author once", got)
	}
}

// One InvalidateTags replaces the author and every page showing them — the pages
// carry the tag without the handler returning it.
func TestCached_InvalidatesWithThePages(t *testing.T) {
	site := newAuthorSite(t, false)
	for i := range 30 {
		site.get(t, i, false)
	}
	site.mu.Lock()
	site.names["A"] = "Ada Lovelace"
	site.mu.Unlock()

	dropped, err := site.app.InvalidateTagsN(context.Background(), "author:A")
	if err != nil || dropped != 20 {
		t.Fatalf("InvalidateTagsN = %d, %v; want A's twenty pages dropped", dropped, err)
	}
	// The first request after the invalidation is served the stale author at
	// once, and starts the refresh; a later one shows the new name.
	if body := site.get(t, 3, false); body != "<article><p>Ada</p></article>" {
		t.Errorf("A's page right after the invalidation = %q, want the stale name", body)
	}
	site.eventually(t, 3, "the new name", func(rec *httptest.ResponseRecorder) bool {
		return rec.Body.String() == "<article><p>Ada Lovelace</p></article>"
	})
	if body := site.get(t, 25, false); body != "<article><p>Bo</p></article>" {
		t.Errorf("B's page = %q", body)
	}
	if got := site.fetched(); got["A"] != 2 || got["B"] != 1 {
		t.Errorf("fetched %v, want A twice and B once", got)
	}
}

// A preview fetches fresh and stores nothing, so an editor sees the change and
// nobody else is served it.
func TestCached_APreviewSkipsIt(t *testing.T) {
	site := newAuthorSite(t, false)
	if err := site.app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Preview") != "" {
				collage.SkipCache(r)
			}
			next.ServeHTTP(w, r)
		})
	}); err != nil {
		t.Fatal(err)
	}
	site.get(t, 0, false)
	site.mu.Lock()
	site.names["A"] = "Draft name"
	site.mu.Unlock()

	if body := site.get(t, 1, true); body != "<article><p>Draft name</p></article>" {
		t.Errorf("preview = %q, want the fresh value", body)
	}
	if body := site.get(t, 1, false); body != "<article><p>Ada</p></article>" {
		t.Errorf("reader after the preview = %q, want the stored value: a preview must not store what it fetched", body)
	}
}

// In development nothing outlives a render, as with the page cache.
func TestCached_DevelopmentKeepsNothing(t *testing.T) {
	site := newAuthorSite(t, true)
	for range 3 {
		site.get(t, 0, false)
	}
	if got := site.fetched(); got["A"] != 3 {
		t.Errorf("development fetched %v, want A once per render", got)
	}
}

// A document's handler shares Cached with the pages: a feed and the posts it lists
// fetch an author once between them.
func TestCached_InADocument(t *testing.T) {
	site := newAuthorSite(t, false)
	doc := collage.NewDocument("authors", "text/plain").WithPath("en", "/authors.txt").Dynamic().
		WithHandler(func(ctx context.Context, rc *collage.RenderContext) ([]byte, []string, error) {
			a, err := collage.Cached(rc, authorKey.With("A"), time.Hour, []string{"author:A"}, func(context.Context) (author, error) {
				site.mu.Lock()
				defer site.mu.Unlock()
				site.calls["A"]++
				return author{Name: site.names["A"]}, nil
			})
			return []byte(a.Name), nil, err
		}).Build()
	if err := site.app.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	site.get(t, 0, false)
	for range 3 {
		rec := httptest.NewRecorder()
		site.app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/authors.txt", nil))
		if rec.Body.String() != "Ada" {
			t.Fatalf("document = %q", rec.Body.String())
		}
	}
	if got := site.fetched(); got["A"] != 1 {
		t.Errorf("fetched %v, want A once across the page and three document requests", got)
	}
}

// A document built from stale data is served with no-store and not cached, so
// the next request shows the refreshed name rather than the old one for its TTL.
func TestCached_AStaleDocumentIsNotCached(t *testing.T) {
	site := newAuthorSite(t, false)
	doc := collage.NewDocument("authors", "text/plain").WithPath("en", "/authors.txt").Incremental(time.Hour).
		WithHandler(func(ctx context.Context, rc *collage.RenderContext) ([]byte, []string, error) {
			a, err := collage.Cached(rc, authorKey.With("A"), time.Hour, []string{"author:A"}, func(context.Context) (author, error) {
				site.mu.Lock()
				defer site.mu.Unlock()
				return author{Name: site.names["A"]}, nil
			})
			return []byte(a.Name), nil, err
		}).Build()
	if err := site.app.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	serveDoc := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		site.app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/authors.txt", nil))
		return rec
	}
	if rec := serveDoc(); rec.Body.String() != "Ada" {
		t.Fatalf("warm document = %q", rec.Body.String())
	}
	site.mu.Lock()
	site.names["A"] = "Ada Lovelace"
	site.mu.Unlock()
	site.app.InvalidateTags(context.Background(), "author:A")

	rec := serveDoc()
	if rec.Body.String() != "Ada" {
		t.Fatalf("stale document = %q, want the stale name", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control on a stale document = %q, want no-store", cc)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		rec = serveDoc()
		if rec.Body.String() == "Ada Lovelace" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("document never showed the new name; last: %q", rec.Body.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A method a document does not answer is a 405 in the document's own kind: plain
// text, not the site's HTML error page.
func TestDocument_A405IsPlainText(t *testing.T) {
	site := newAuthorSite(t, false)
	doc := collage.NewDocument("robots", "text/plain").WithPath("en", "/robots.txt").Dynamic().
		WithHandler(func(context.Context, *collage.RenderContext) ([]byte, []string, error) {
			return []byte("User-agent: *"), nil, nil
		}).Build()
	if err := site.app.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	site.app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/robots.txt", nil))
	if rec.Code != http.StatusMethodNotAllowed || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("POST /robots.txt = %d %q, want a plain-text 405", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// Concurrent misses on one document run its handler once, as a page's do.
func TestDocument_ConcurrentMissesAreCoalesced(t *testing.T) {
	var runs atomic.Int32
	release := make(chan struct{})
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/x.html": {Data: []byte(`x`)}}, Root: "t"},
		Cache:    collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc := collage.NewDocument("feed", "application/xml").WithPath("en", "/feed.xml").Incremental(time.Minute).
		WithHandler(func(context.Context, *collage.RenderContext) ([]byte, []string, error) {
			runs.Add(1)
			<-release
			return []byte("<rss/>"), nil, nil
		}).Build()
	if err := app.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/feed.xml", nil))
			if rec.Body.String() != "<rss/>" {
				t.Errorf("body = %q", rec.Body.String())
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if runs.Load() != 1 {
		t.Errorf("handler runs = %d, want 1 for ten concurrent misses", runs.Load())
	}
}

// A page built from stale data is served, but neither cached here nor at a CDN:
// otherwise the old name would be pinned for the page's whole TTL.
func TestCached_TheRequestAfterAStaleOneIsFresh(t *testing.T) {
	site := newAuthorSite(t, false)
	site.get(t, 3, false)
	site.mu.Lock()
	site.names["A"] = "Ada Lovelace"
	site.mu.Unlock()
	site.app.InvalidateTags(context.Background(), "author:A")

	rec := site.serve(t, 3)
	if rec.Body.String() != "<article><p>Ada</p></article>" {
		t.Fatalf("stale render = %q", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control on a stale render = %q, want no-store", cc)
	}
	site.eventually(t, 3, "the new name", func(rec *httptest.ResponseRecorder) bool {
		return rec.Body.String() == "<article><p>Ada Lovelace</p></article>"
	})
}

// The point of it all: invalidated, upstream down, the site still answers.
func TestCached_AnInvalidatedPageRendersWhileTheUpstreamIsDown(t *testing.T) {
	site := newAuthorSite(t, false)
	for i := range 30 {
		site.get(t, i, false)
	}
	site.fail(errors.New("cms down"))
	site.app.InvalidateTags(context.Background(), "author:A", "author:B")
	for i := range 30 {
		rec := site.serve(t, i)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<p>") {
			t.Fatalf("page %d with the upstream down = %d %q", i, rec.Code, rec.Body.String())
		}
	}
}

// Content removed upstream leaves the site: not found never falls back. The
// author card sits in a required slot, so its data's not found is the page's
// 404 (see pkg/collage/fragmentpath_test.go for the rule this relies on; if the
// card's failure is a 500 here instead, make the card's slot hold a Required
// fragment as that test does - the point under test is that the page stops
// showing the stale author, not which status the slot policy picks).
func TestCached_NotFoundUpstreamBecomesA404(t *testing.T) {
	site := newAuthorSite(t, false)
	site.get(t, 3, false)
	site.fail(fmt.Errorf("author A: %w", collage.ErrNotFound))
	site.app.InvalidateTags(context.Background(), "author:A")
	site.eventually(t, 3, "a 404", func(rec *httptest.ResponseRecorder) bool {
		return rec.Code == http.StatusNotFound
	})
}

// An export writes files nobody re-renders, so it waits for fresh data.
func TestCached_AnExportAfterAnInvalidationIsFresh(t *testing.T) {
	site := newAuthorSite(t, false)
	site.get(t, 3, false)
	site.mu.Lock()
	site.names["A"] = "Ada Lovelace"
	site.mu.Unlock()
	site.app.InvalidateTags(context.Background(), "author:A")
	out := t.TempDir()
	if _, err := collage.Build(context.Background(), site.app, collage.BuildOptions{OutDir: out, Concurrency: 4}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(out, "p", "3", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Ada Lovelace") {
		t.Errorf("exported page = %q, want the new name", data)
	}
}

func diskCache(dir, version string) collage.CacheConfig {
	return collage.CacheConfig{Enabled: true, Type: "disk", Dir: dir, Version: version, DefaultTTL: time.Hour}
}

// A new deploy — a new build, so a cold page cache — with its upstream down
// still renders, from the data the last one fetched.
func TestCached_ARestartRendersFromPersistedData(t *testing.T) {
	dir := t.TempDir()
	first := newAuthorSiteWith(t, false, diskCache(dir, "build-1"))
	first.get(t, 3, false)
	if err := first.app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The upstream is down, and its fetches hang until released: a render that
	// waited on one would never come back.
	second := newAuthorSiteWith(t, false, diskCache(dir, "build-2"))
	second.fail(errors.New("cms down"))
	gate := second.hold()
	rec := second.serve(t, 3)
	close(gate)
	if rec.Code != http.StatusOK || rec.Body.String() != "<article><p>Ada</p></article>" {
		t.Fatalf("after restart = %d %q", rec.Code, rec.Body.String())
	}
	// Served stale: the value is refreshed in the background, and that fetch
	// fails there, not in the page.
	if err := second.app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if second.attempts.Load() == 0 {
		t.Error("a restored value was not refreshed; it would be served unchanged for good")
	}
}

// A data directory that cannot be made costs persistence, not the site.
func TestCached_AnUnusableDataDirectoryFallsBackToMemory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	site := newAuthorSiteWith(t, false, diskCache(dir, "b"))
	if rec := site.serve(t, 3); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

// PurgeTags is the hard delete: nothing stale is left to serve.
func TestPurgeTags_LeavesNothingToServe(t *testing.T) {
	site := newAuthorSite(t, false)
	site.get(t, 3, false)
	site.mu.Lock()
	site.names["A"] = "Ada Lovelace"
	site.mu.Unlock()
	if err := site.app.PurgeTags(context.Background(), "author:A"); err != nil {
		t.Fatal(err)
	}
	if body := site.get(t, 3, false); body != "<article><p>Ada Lovelace</p></article>" {
		t.Errorf("after PurgeTags = %q, want the new name at once", body)
	}
}

// Shutdown waits for a background refresh before the plugins stop, so the
// refresh never runs against a resource a plugin has released.
func TestCached_ShutdownWaitsForARunningRefresh(t *testing.T) {
	site := newAuthorSite(t, false)
	site.get(t, 3, false)
	gate := site.hold()
	if err := site.app.InvalidateTags(context.Background(), "author:A"); err != nil {
		t.Fatal(err)
	}
	if rec := site.serve(t, 3); rec.Code != http.StatusOK {
		t.Fatalf("stale render = %d", rec.Code)
	}

	done := make(chan error, 1)
	go func() { done <- site.app.Shutdown(context.Background()) }()
	select {
	case err := <-done:
		close(gate)
		t.Fatalf("Shutdown returned %v with a refresh running", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
