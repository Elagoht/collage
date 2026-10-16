package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/types"
)

// withEpoch backs Deps.Invalidations with epoch, as both counts: a test advances
// it to stand for an invalidation that started and finished at once.
func withEpoch(epoch *atomic.Uint64) envOption {
	return func(d *Deps) {
		d.Invalidations = func() (uint64, uint64) {
			n := epoch.Load()
			return n, n
		}
	}
}

// staleEnv is a cached page "/" warmed with "<html>old</html>", whose entry is then
// marked stale and whose next render fails, which is the state every test here
// starts from.
func staleEnv(t *testing.T, opts ...envOption) (*testEnv, string) {
	t.Helper()
	page := testPage("home", "/", types.StrategyIncremental)
	env := newEnv(t, []*types.Page{page}, opts...)
	env.engine.set("home", fakeRender{html: "<html>old</html>"})
	if rec := env.get("/"); rec.Code != http.StatusOK {
		t.Fatalf("warm = %d", rec.Code)
	}
	key := flightKeyFor(t, env, "/")
	if err := env.cache.MarkStaleKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	env.engine.set("home", fakeRender{err: errors.New("cms down")})
	return env, key
}

// cacheEvents counts the recorded CacheEvent calls of kind event.
func cacheEvents(env *testEnv, event observability.CacheEvent) int {
	n := 0
	for _, call := range env.metrics.Snapshot().CacheEvents {
		if call.Event == event {
			n++
		}
	}
	return n
}

const (
	staleLog       = "collage: serving a stale page, its render failed"
	staleDataLog   = "collage: serving a stale page while its data refreshes"
	staleDocLog    = "collage: serving a stale document, its render failed"
	staleDocData   = "collage: serving a stale document while its data refreshes"
	requestFailLog = "collage: request failed"
)

// levelCount counts the records logged at level, whatever their message.
func levelCount(logs *countingHandler, level slog.Level) int {
	logs.mu.Lock()
	defer logs.mu.Unlock()
	n := 0
	for _, rec := range logs.records {
		if rec.level == level {
			n++
		}
	}
	return n
}

// recordsAt returns the records carrying msg logged at level.
func recordsAt(logs *countingHandler, msg string, level slog.Level) int {
	n := 0
	for _, rec := range logs.recordsFor(msg) {
		if rec.level == level {
			n++
		}
	}
	return n
}

// A render degraded only because it read stale cached data is the data store
// refreshing in the background, which is the stale cache working as it should:
// the stale page it falls back to is a debug line, not a warning, and no error.
func TestHandler_AStaleDataFallbackLogsAtDebug(t *testing.T) {
	recorder := &recordingPlugin{}
	env, _ := staleEnv(t, withPlugins(t, recorder))
	env.engine.set("home", fakeRender{html: "<html>from stale data</html>", stale: true})
	rec := env.get("/")
	if rec.Body.String() != "<html>old</html>" || rec.Header().Get(CacheStatusHeader) != cacheStale {
		t.Fatalf("stale-data render over a stale entry = %q %s=%q, want the stale page",
			rec.Body.String(), CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
	}
	if got := env.logs.count(staleLog); got != 0 {
		t.Errorf("stale warnings = %d, want none for a render that only read stale data", got)
	}
	if got := recordsAt(env.logs, staleDataLog, slog.LevelDebug); got != 1 {
		t.Errorf("debug %q lines = %d, want 1", staleDataLog, got)
	}
	if got := levelCount(env.logs, slog.LevelWarn) + levelCount(env.logs, slog.LevelError); got != 0 {
		t.Errorf("warn/error lines = %d, want none", got)
	}
	if errs := recorder.reportedErrors(); len(errs) != 0 {
		t.Errorf("error hook got %v, want nothing", errs)
	}
	if got := cacheEvents(env, observability.CacheStale); got != 1 {
		t.Errorf("CacheStale events = %d, want 1", got)
	}
}

// A failed render a stale page hides is one log line, the warning, and one call to
// the error hooks: not also the error-level "request failed" an error page would
// have logged, since no error page was served.
func TestHandler_AHiddenStaleFailureLogsOneWarning(t *testing.T) {
	recorder := &recordingPlugin{}
	env, _ := staleEnv(t, withPlugins(t, recorder))
	rec := env.get("/")
	if rec.Header().Get(CacheStatusHeader) != cacheStale {
		t.Fatalf("%s = %q, want STALE", CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
	}
	if got := recordsAt(env.logs, staleLog, slog.LevelWarn); got != 1 {
		t.Errorf("stale warnings = %d, want 1", got)
	}
	if got := levelCount(env.logs, slog.LevelError); got != 0 {
		t.Errorf("error-level lines = %d (%d %q), want none", got, env.logs.count(requestFailLog), requestFailLog)
	}
	if got := levelCount(env.logs, slog.LevelWarn); got != 1 {
		t.Errorf("warn lines = %d, want exactly the one stale warning", got)
	}
	if errs := recorder.reportedErrors(); len(errs) != 1 {
		t.Errorf("error hook got %v, want the failure once", errs)
	}
}

// staleDocEngine is a fakeEngine that also answers documents, with result.
type staleDocEngine struct {
	*fakeEngine
	mu     sync.Mutex
	result render.DocumentResult
	err    error
}

func (e *staleDocEngine) set(result render.DocumentResult, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.result, e.err = result, err
}

func (e *staleDocEngine) ExecuteDocument(_ context.Context, doc *types.Document, _ *types.RenderContext) (*render.DocumentResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := e.result
	result.ContentType = doc.ContentType
	return &result, e.err
}

// staleDocEnv is a cached document at /feed.xml warmed with "old", whose entry is
// then marked stale.
func staleDocEnv(t *testing.T, opts ...envOption) (*testEnv, *staleDocEngine) {
	t.Helper()
	doc := &types.Document{
		Name:        "feed",
		ContentType: "application/xml",
		Paths:       map[string]string{"en": "/feed.xml"},
		Strategy:    types.StrategyIncremental,
		CacheTTL:    time.Hour,
		Handler: func(context.Context, *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, nil
		},
	}
	engine := &staleDocEngine{fakeEngine: newFakeEngine(fakeRender{})}
	engine.set(render.DocumentResult{Body: []byte("old"), Tags: []string{"feed"}}, nil)
	rt := router.New(router.LocaleOptions{Default: "en"})
	if err := rt.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	opts = append([]envOption{func(d *Deps) { d.Router, d.Renderer = rt, engine }}, opts...)
	env := newEnv(t, nil, opts...)
	if rec := env.get("/feed.xml"); rec.Code != http.StatusOK {
		t.Fatalf("warm = %d", rec.Code)
	}
	keys, err := env.tracker.Resolve(context.Background(), []string{"feed"})
	if err != nil || len(keys) != 1 {
		t.Fatalf("Resolve = %v, %v", keys, err)
	}
	if err := env.cache.MarkStaleKey(context.Background(), keys[0]); err != nil {
		t.Fatal(err)
	}
	return env, engine
}

// A document is the same: stale data alone is debug, a failure a warning.
func TestHandler_AStaleDocumentLogsByWhyItIsStale(t *testing.T) {
	recorder := &recordingPlugin{}
	env, engine := staleDocEnv(t, withPlugins(t, recorder))

	engine.set(render.DocumentResult{Body: []byte("from stale data"), Stale: true}, nil)
	if rec := env.get("/feed.xml"); rec.Body.String() != "old" || rec.Header().Get(CacheStatusHeader) != cacheStale {
		t.Fatalf("stale-data document = %q %s=%q, want the stale entry", rec.Body.String(),
			CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
	}
	if got := recordsAt(env.logs, staleDocData, slog.LevelDebug); got != 1 {
		t.Errorf("debug %q lines = %d, want 1", staleDocData, got)
	}
	if got := levelCount(env.logs, slog.LevelWarn) + levelCount(env.logs, slog.LevelError); got != 0 {
		t.Errorf("warn/error lines = %d after a stale-data document, want none", got)
	}

	engine.set(render.DocumentResult{}, errors.New("feed down"))
	if rec := env.get("/feed.xml"); rec.Body.String() != "old" || rec.Header().Get(CacheStatusHeader) != cacheStale {
		t.Fatalf("failed document = %q %s=%q, want the stale entry", rec.Body.String(),
			CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
	}
	if got := recordsAt(env.logs, staleDocLog, slog.LevelWarn); got != 1 {
		t.Errorf("stale document warnings = %d, want 1", got)
	}
	if got := levelCount(env.logs, slog.LevelError); got != 0 {
		t.Errorf("error-level lines = %d, want none", got)
	}
	if errs := recorder.reportedErrors(); len(errs) != 1 {
		t.Errorf("error hook got %v, want the failure once", errs)
	}
}

// A stale serve is a miss to the cache, one lookup's worth, and a stale event and
// a warning to whoever watches the site: an outage hidden from readers must not
// be hidden from the operator too.
func TestHandler_AStaleServeIsCountedAndLogged(t *testing.T) {
	env, key := staleEnv(t)
	before := env.cache.Stats()
	hits, misses := cacheEvents(env, observability.CacheHit), cacheEvents(env, observability.CacheMiss)

	rec := env.get("/")
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>old</html>" {
		t.Fatalf("stale serve = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(CacheStatusHeader); got != cacheStale {
		t.Errorf("%s = %q, want STALE", CacheStatusHeader, got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("ETag"); got != cache.ETag([]byte("<html>old</html>")) {
		t.Errorf("ETag = %q, want the stale entry's", got)
	}

	after := env.cache.Stats()
	if after.Hits != before.Hits || after.Misses != before.Misses+1 {
		t.Errorf("cache stats moved hits %d→%d misses %d→%d, want one miss and no hit",
			before.Hits, after.Hits, before.Misses, after.Misses)
	}
	if got := cacheEvents(env, observability.CacheHit); got != hits {
		t.Errorf("CacheHit events %d→%d, want none for a stale entry", hits, got)
	}
	if got := cacheEvents(env, observability.CacheMiss); got != misses+1 {
		t.Errorf("CacheMiss events %d→%d, want one", misses, got)
	}
	var stale []string
	for _, call := range env.metrics.Snapshot().CacheEvents {
		if call.Event == observability.CacheStale {
			stale = append(stale, call.Key)
		}
	}
	if len(stale) != 1 || stale[0] != key {
		t.Errorf("CacheStale events = %q, want one for %q", stale, key)
	}
	if got := env.logs.count(staleLog); got != 1 {
		t.Errorf("stale warnings = %d, want 1", got)
	}
	// The entry is still there for the next failure.
	if _, _, isStale, found := env.cache.GetStale(context.Background(), key); !found || !isStale {
		t.Errorf("after a stale serve the entry is found=%v stale=%v, want kept stale", found, isStale)
	}
}

// A render that succeeds replaces the stale entry and is a MISS.
func TestHandler_AStaleEntryIsReplacedByASuccessfulRender(t *testing.T) {
	env, key := staleEnv(t)
	env.engine.set("home", fakeRender{html: "<html>new</html>"})
	rec := env.get("/")
	if rec.Body.String() != "<html>new</html>" || rec.Header().Get(CacheStatusHeader) != cacheMiss {
		t.Fatalf("render over a stale entry = %q %s=%q, want the new page, MISS",
			rec.Body.String(), CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
	}
	if content, _, isStale, found := env.cache.GetStale(context.Background(), key); !found || isStale || string(content) != "<html>new</html>" {
		t.Errorf("entry = %q found=%v stale=%v, want the new page fresh", content, found, isStale)
	}
	if got := env.get("/").Header().Get(CacheStatusHeader); got != cacheHit {
		t.Errorf("next request %s = %q, want HIT", CacheStatusHeader, got)
	}
}

// A render that started before an invalidation may have read what it invalidated,
// so it is served to the reader who waited for it but not stored.
func TestHandler_ARenderOverlappingAnInvalidationIsNotStored(t *testing.T) {
	page := testPage("home", "/", types.StrategyIncremental)
	inner := newFakeEngine(fakeRender{html: "<html>home</html>"})
	blocking := newBlockingEngine(inner)
	var epoch atomic.Uint64
	env := newEnv(t, []*types.Page{page}, withEpoch(&epoch), func(d *Deps) { d.Renderer = blocking })

	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- env.get("/") }()
	<-blocking.started
	epoch.Add(1)
	close(blocking.release)
	rec := <-done

	if rec.Code != http.StatusOK || rec.Body.String() != "<html>home</html>" {
		t.Fatalf("overlapped render = %d %q, want it served", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(CacheStatusHeader); got != cacheMiss {
		t.Errorf("%s = %q, want MISS", CacheStatusHeader, got)
	}
	if n := env.cacheEntries(); n != 0 {
		t.Errorf("cache holds %d entries, want none: an overlapped render was stored", n)
	}
	env.get("/")
	if got := inner.pageCalls("home"); got != 2 {
		t.Errorf("renders = %d, want 2: the request after it must render again", got)
	}
	if n := env.cacheEntries(); n != 1 {
		t.Errorf("cache holds %d entries after an undisturbed render, want 1", n)
	}
}

// A render begun while an invalidation is under way — started ahead of finished —
// is served but not stored, even when that invalidation ends before the render
// does and no other begins: it may have read what the invalidation replaced.
func TestHandler_ARenderBegunMidInvalidationIsNotStoredStaleOrFresh(t *testing.T) {
	page := testPage("home", "/", types.StrategyIncremental)
	var started, finished atomic.Uint64
	started.Add(1)
	inner := newFakeEngine(fakeRender{html: "<html>home</html>"})
	blocking := newBlockingEngine(inner)
	env := newEnv(t, []*types.Page{page}, func(d *Deps) {
		d.Renderer = blocking
		d.Invalidations = func() (uint64, uint64) {
			f := finished.Load()
			return started.Load(), f
		}
	})

	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- env.get("/") }()
	<-blocking.started
	finished.Add(1)
	close(blocking.release)
	if rec := <-done; rec.Code != http.StatusOK || rec.Body.String() != "<html>home</html>" {
		t.Fatalf("render begun mid-invalidation = %d %q, want it served", rec.Code, rec.Body.String())
	}
	if n := env.cacheEntries(); n != 0 {
		t.Errorf("cache holds %d entries, want none: a render begun mid-invalidation was stored", n)
	}
	env.get("/")
	if n := env.cacheEntries(); n != 1 {
		t.Errorf("cache holds %d entries after an undisturbed render, want 1", n)
	}
}

// epochBumpingCache advances an epoch inside SetTagged, before writing: an
// invalidation landing between writeCache's epoch check and its write, which the
// check alone cannot see.
type epochBumpingCache struct {
	inner *cache.MemoryCache
	epoch *atomic.Uint64
}

func (c *epochBumpingCache) Get(ctx context.Context, key string) ([]byte, string, bool) {
	return c.inner.Get(ctx, key)
}

func (c *epochBumpingCache) Set(ctx context.Context, key string, content []byte, ttl time.Duration) (string, error) {
	c.epoch.Add(1)
	return c.inner.Set(ctx, key, content, ttl)
}

func (c *epochBumpingCache) SetTagged(ctx context.Context, key string, content []byte, ttl time.Duration, tags []string) (string, error) {
	c.epoch.Add(1)
	return c.inner.SetTagged(ctx, key, content, ttl, tags)
}

func (c *epochBumpingCache) Invalidate(ctx context.Context, tags []string) error {
	return c.inner.Invalidate(ctx, tags)
}

func (c *epochBumpingCache) InvalidateKey(ctx context.Context, key string) error {
	return c.inner.InvalidateKey(ctx, key)
}

func (c *epochBumpingCache) Clear(ctx context.Context) error { return c.inner.Clear(ctx) }

func TestHandler_AnInvalidationDuringTheWriteDropsIt(t *testing.T) {
	page := testPage("home", "/", types.StrategyIncremental)
	var epoch atomic.Uint64
	inner := cache.NewMemory(cache.MemoryConfig{})
	env := newEnv(t, []*types.Page{page}, withEpoch(&epoch),
		func(d *Deps) { d.Cache = &epochBumpingCache{inner: inner, epoch: &epoch} })
	if rec := env.get("/"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if n := inner.Stats().Entries; n != 0 {
		t.Errorf("cache holds %d entries, want none: a write an invalidation overlapped was kept", n)
	}
}

// A stale entry stored under another forgery key is dropped as a fresh one is:
// served, its form would carry a marker nothing replaces.
func TestHandler_AStaleEntryWithAForeignMarkerIsDropped(t *testing.T) {
	option, guard := withCSRF(t)
	env, key := staleEnv(t, option)
	foreign := "collage-csrf-" + strings.Repeat("x", 32)
	if foreign == guard.Marker() {
		t.Fatal("the test's foreign marker is the guard's own")
	}
	ctx := context.Background()
	if _, err := env.cache.SetTagged(ctx, key, []byte(`<form><input value="`+foreign+`"></form>`), time.Hour, nil); err != nil {
		t.Fatal(err)
	}
	if err := env.cache.MarkStaleKey(ctx, key); err != nil {
		t.Fatal(err)
	}

	rec := env.get("/")
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), foreign) {
		t.Errorf("stale entry with a foreign marker = %d %q, want the render's failure", rec.Code, rec.Body.String())
	}
	if _, _, _, found := env.cache.GetStale(ctx, key); found {
		t.Error("the stale entry with a foreign marker was kept")
	}
}

// A stale page carries this reader's forgery token, as a hit does.
func TestHandler_AStalePageIsPersonalised(t *testing.T) {
	option, guard := withCSRF(t)
	env, key := staleEnv(t, option)
	ctx := context.Background()
	if _, err := env.cache.SetTagged(ctx, key, []byte(`<form><input value="`+guard.Marker()+`"></form>`), time.Hour, nil); err != nil {
		t.Fatal(err)
	}
	if err := env.cache.MarkStaleKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	rec := env.get("/")
	if rec.Code != http.StatusOK || rec.Header().Get(CacheStatusHeader) != cacheStale {
		t.Fatalf("stale form page = %d %s=%q", rec.Code, CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
	}
	if strings.Contains(rec.Body.String(), guard.Marker()) {
		t.Errorf("stale body still carries the marker: %q", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control = %q, want private, no-store", got)
	}
}

// HEAD on a stale page answers as GET would, without a body, and a conditional
// request for the stale copy is a 304 that says STALE.
func TestHandler_HeadOnAStalePage(t *testing.T) {
	env, key := staleEnv(t)
	rec := env.do(httptest.NewRequest(http.MethodHead, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD = %d", rec.Code)
	}
	if got := rec.Header().Get(CacheStatusHeader); got != cacheStale {
		t.Errorf("HEAD %s = %q, want STALE", CacheStatusHeader, got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("HEAD Cache-Control = %q, want no-store", got)
	}
	etag := rec.Header().Get("ETag")
	if etag != cache.ETag([]byte("<html>old</html>")) {
		t.Errorf("HEAD ETag = %q, want the stale entry's", etag)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	rec = env.do(req)
	if rec.Code != http.StatusNotModified || rec.Header().Get(CacheStatusHeader) != cacheStale {
		t.Errorf("conditional GET = %d %s=%q, want 304 STALE", rec.Code, CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
	}

	// A HEAD whose render succeeds is a MISS, and stores nothing: it has no body.
	env.engine.set("home", fakeRender{html: "<html>new</html>"})
	rec = env.do(httptest.NewRequest(http.MethodHead, "/", nil))
	if rec.Code != http.StatusOK || rec.Header().Get(CacheStatusHeader) != cacheMiss {
		t.Errorf("HEAD, render back = %d %s=%q, want 200 MISS", rec.Code, CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
	}
	if _, _, isStale, found := env.cache.GetStale(context.Background(), key); !found || !isStale {
		t.Errorf("after a HEAD the entry is found=%v stale=%v, want it still stale", found, isStale)
	}
}

// Every request that joined one failed render falls back to the stale entry it
// found, and each stale serve is counted.
func TestHandler_WaitersOfAFailedRenderEachServeTheStalePage(t *testing.T) {
	page := testPage("home", "/", types.StrategyIncremental)
	inner := newFakeEngine(fakeRender{err: errors.New("cms down")})
	blocking := newBlockingEngine(inner)
	recorder := &recordingPlugin{}
	env := newEnv(t, []*types.Page{page}, withPlugins(t, recorder), func(d *Deps) { d.Renderer = blocking })
	key := flightKeyFor(t, env, "/")
	ctx := context.Background()
	if _, err := env.cache.SetTagged(ctx, key, []byte("<html>old</html>"), time.Hour, nil); err != nil {
		t.Fatal(err)
	}
	if err := env.cache.MarkStaleKey(ctx, key); err != nil {
		t.Fatal(err)
	}

	const readers = 8
	recs := make([]*httptest.ResponseRecorder, readers)
	var wg sync.WaitGroup
	wg.Go(func() { recs[0] = env.get("/") })
	<-blocking.started
	for i := 1; i < readers; i++ {
		wg.Go(func() { recs[i] = env.get("/") })
	}
	awaitWaiters(t, env.handler.flight, key, readers-1)
	close(blocking.release)
	wg.Wait()

	for i, rec := range recs {
		if rec.Code != http.StatusOK || rec.Body.String() != "<html>old</html>" || rec.Header().Get(CacheStatusHeader) != cacheStale {
			t.Errorf("reader %d = %d %q %s=%q, want the stale page", i, rec.Code, rec.Body.String(),
				CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
		}
	}
	if got := inner.pageCalls("home"); got != 1 {
		t.Errorf("renders = %d, want 1", got)
	}
	if got := cacheEvents(env, observability.CacheStale); got != readers {
		t.Errorf("CacheStale events = %d, want one per reader (%d)", got, readers)
	}
	// One render failed, so one outage is reported, however many readers it
	// was hidden from.
	if errs := recorder.reportedErrors(); len(errs) != 1 || errs[0] == nil || errs[0].Error() != "cms down" {
		t.Errorf("error hook got %v, want the one render's failure once", errs)
	}
	if got := recorder.recorded(); !slices.Contains(got, "Error:"+stageRender) {
		t.Errorf("hooks = %q, want an Error at the render stage", got)
	}
	if got := env.logs.count(staleLog); got != 1 {
		t.Errorf("stale warnings = %d, want 1 for one failed render", got)
	}
}

// A degraded render falling back to the stale page has no error to hand the
// error hooks: its fragment failure is the render engine's to report.
func TestHandler_ADegradedStaleFallbackFiresNoErrorHook(t *testing.T) {
	recorder := &recordingPlugin{}
	env, _ := staleEnv(t, withPlugins(t, recorder))
	env.engine.set("home", fakeRender{html: "<html>holed</html>", degraded: "aside", err: nil})
	rec := env.get("/")
	if rec.Body.String() != "<html>old</html>" || rec.Header().Get(CacheStatusHeader) != cacheStale {
		t.Fatalf("degraded over a stale entry = %q %s=%q, want the stale page",
			rec.Body.String(), CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
	}
	if errs := recorder.reportedErrors(); len(errs) != 0 {
		t.Errorf("error hook got %v for a degraded fallback, want nothing", errs)
	}
	if got := env.logs.count(staleLog); got != 1 {
		t.Errorf("stale warnings = %d, want 1", got)
	}
}

// A render that answers not found drops the stale entry and is the 404.
func TestHandler_ANotFoundRenderDropsTheStaleEntry(t *testing.T) {
	env, key := staleEnv(t)
	env.engine.set("home", fakeRender{err: errors.New("gone"), notFound: true})
	rec := env.get("/")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get(CacheStatusHeader); got != "" {
		t.Errorf("404 carries %s = %q, want none", CacheStatusHeader, got)
	}
	if _, _, _, found := env.cache.GetStale(context.Background(), key); found {
		t.Error("the stale entry survived a not found")
	}
}

// A store that is not a StaleCache is read with Get alone, so an entry it marks
// nothing about is never STALE.
func TestHandler_APlainCacheIsNeverStale(t *testing.T) {
	page := testPage("home", "/", types.StrategyIncremental)
	inner := cache.NewMemory(cache.MemoryConfig{})
	env := newEnv(t, []*types.Page{page}, func(d *Deps) { d.Cache = &plainCache{inner: inner} })
	env.get("/")
	key := flightKeyFor(t, env, "/")
	if err := inner.MarkStaleKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	env.engine.set("home", fakeRender{err: errors.New("cms down")})
	rec := env.get("/")
	if rec.Code != http.StatusInternalServerError || rec.Header().Get(CacheStatusHeader) != "" {
		t.Errorf("plain cache, failing render = %d %s=%q, want a 500 without the header",
			rec.Code, CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
	}
}
