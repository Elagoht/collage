package core

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/types"
)

// plainCache is a store implementing only cache.Cache, as an application's own
// store may: it cannot keep a stale entry, so invalidation must still drop.
type plainCache struct {
	inner *cache.MemoryCache
}

func (p plainCache) Get(ctx context.Context, key string) ([]byte, string, bool) {
	return p.inner.Get(ctx, key)
}

func (p plainCache) Set(ctx context.Context, key string, content []byte, ttl time.Duration) (string, error) {
	return p.inner.Set(ctx, key, content, ttl)
}

func (p plainCache) InvalidateKey(ctx context.Context, key string) error {
	return p.inner.InvalidateKey(ctx, key)
}

func (p plainCache) Clear(ctx context.Context) error { return p.inner.Clear(ctx) }

// warmedHome builds an app over store (the default memory store when nil),
// renders the home page once and returns the app with the page's cache key.
func warmedHome(t *testing.T, store cache.Cache) (*App, string) {
	t.Helper()
	return warmedHomeWith(t, func(cfg *Config) {
		if store != nil {
			cfg.Cache.Store = store
		}
	})
}

// warmedHomeWith is warmedHome with the configuration changed by mutate.
func warmedHomeWith(t *testing.T, mutate func(*Config)) (*App, string) {
	t.Helper()
	app := newTestApp(t, mutate)
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if rec := get(app.Handler(), "/"); rec.Code != 200 {
		t.Fatalf("warming status = %d", rec.Code)
	}
	keys, err := app.tracker.Resolve(context.Background(), []string{"homepage"})
	if err != nil || len(keys) != 1 {
		t.Fatalf("Resolve = %v, %v, want one key", keys, err)
	}
	return app, keys[0]
}

func TestInvalidateTags_MarksPagesStaleOnAStaleCache(t *testing.T) {
	app, key := warmedHome(t, nil)
	ctx := context.Background()

	if err := app.InvalidateTags(ctx, "homepage"); err != nil {
		t.Fatalf("InvalidateTags: %v", err)
	}
	stale, ok := app.store.(cache.StaleCache)
	if !ok {
		t.Fatalf("built-in store %T is not a StaleCache", app.store)
	}
	if _, _, found := stale.Get(ctx, key); found {
		t.Fatal("Get found the page after invalidation, want a miss")
	}
	if _, _, isStale, found := stale.GetStale(ctx, key); !found || !isStale {
		t.Fatalf("GetStale found=%v stale=%v, want the page kept and stale", found, isStale)
	}
}

// A purge after an invalidation reaches the stale page through the store's own
// tag index, because the tracker has already forgotten it.
func TestPurgeTags_RemovesStalePages(t *testing.T) {
	stores := map[string]func(*Config){
		"memory": nil,
		"tiered disk": func(cfg *Config) {
			cfg.Cache.Type = "disk"
			cfg.Cache.Dir = t.TempDir()
		},
	}
	for name, mutate := range stores {
		t.Run(name, func(t *testing.T) {
			app, key := warmedHomeWith(t, mutate)
			ctx := context.Background()
			stale, ok := app.store.(cache.StaleCache)
			if !ok {
				t.Fatalf("store %T is not a StaleCache", app.store)
			}

			if err := app.InvalidateTags(ctx, "homepage"); err != nil {
				t.Fatalf("InvalidateTags: %v", err)
			}
			if _, _, isStale, found := stale.GetStale(ctx, key); !found || !isStale {
				t.Fatalf("before the purge: found=%v stale=%v, want a stale page", found, isStale)
			}
			if err := app.PurgeTags(ctx, "homepage"); err != nil {
				t.Fatalf("PurgeTags: %v", err)
			}
			if _, _, _, found := stale.GetStale(ctx, key); found {
				t.Fatal("GetStale found the page after a purge, want nothing")
			}
		})
	}
}

func TestInvalidateTags_ACustomStoreStillDrops(t *testing.T) {
	inner := cache.NewMemory(cache.MemoryConfig{})
	app, key := warmedHome(t, plainCache{inner: inner})

	n, err := app.InvalidateTagsN(context.Background(), "homepage")
	if err != nil || n != 1 {
		t.Fatalf("InvalidateTagsN = %d, %v, want 1, nil", n, err)
	}
	if _, _, found := inner.Get(context.Background(), key); found {
		t.Fatal("custom store still holds the page")
	}
	if _, _, _, found := inner.GetStale(context.Background(), key); found {
		t.Fatal("custom store kept the page stale, want it removed")
	}
}

func TestInvalidations_CountEachCallStartedAndFinished(t *testing.T) {
	app, _ := warmedHome(t, nil)
	ctx := context.Background()

	started, finished := app.invalidations()
	if started != finished {
		t.Fatalf("at rest started=%d finished=%d, want them equal", started, finished)
	}
	_ = app.InvalidateTags(ctx, "homepage")
	_ = app.PurgeTags(ctx, "homepage")
	if s, f := app.invalidations(); s != started+2 || f != finished+2 {
		t.Fatalf("after two calls started=%d finished=%d, want %d and %d", s, f, started+2, finished+2)
	}
	_ = app.InvalidateTags(ctx)
	if s, f := app.invalidations(); s != started+2 || f != finished+2 {
		t.Fatalf("after an empty call started=%d finished=%d, want them unchanged", s, f)
	}
}

// taggedOnlyCache implements cache.Cache and cache.TaggedCache but not
// cache.StaleCache, and records the tags Invalidate received.
type taggedOnlyCache struct {
	plainCache
	invalidated [][]string
}

func (c *taggedOnlyCache) SetTagged(ctx context.Context, key string, content []byte, ttl time.Duration, _ []string) (string, error) {
	return c.Set(ctx, key, content, ttl)
}

func (c *taggedOnlyCache) Invalidate(ctx context.Context, tags []string) error {
	c.invalidated = append(c.invalidated, append([]string(nil), tags...))
	return nil
}

// A TaggedCache that cannot keep stale entries still has Invalidate called with
// the tags, and its pages are dropped through InvalidateKey as before.
func TestInvalidateTags_ATaggedStoreWithoutStaleStillInvalidates(t *testing.T) {
	inner := cache.NewMemory(cache.MemoryConfig{})
	store := &taggedOnlyCache{plainCache: plainCache{inner: inner}}
	app, key := warmedHome(t, store)
	ctx := context.Background()

	if err := app.InvalidateTags(ctx, "homepage"); err != nil {
		t.Fatalf("InvalidateTags: %v", err)
	}
	if len(store.invalidated) != 1 || len(store.invalidated[0]) != 1 || store.invalidated[0][0] != "homepage" {
		t.Fatalf("Invalidate calls = %v, want one call carrying [homepage]", store.invalidated)
	}
	if _, _, _, found := inner.GetStale(ctx, key); found {
		t.Fatal("the page was kept, want it dropped: the store is not a StaleCache")
	}
}

// blockingMarkCache is the built-in memory store with MarkStaleKey held open the
// first time it is called, so a test can act while an invalidation is under way.
type blockingMarkCache struct {
	*cache.MemoryCache
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (c *blockingMarkCache) MarkStaleKey(ctx context.Context, key string) error {
	c.once.Do(func() {
		close(c.entered)
		<-c.release
	})
	return c.MemoryCache.MarkStaleKey(ctx, key)
}

// pausingPlugin holds the first render of path open in BeforeRender: after the
// handler has noted the invalidations, before anything is read.
type pausingPlugin struct {
	path    string
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (*pausingPlugin) Name() string                            { return "acme/pause" }
func (*pausingPlugin) Version() string                         { return "1.0.0" }
func (*pausingPlugin) Init(context.Context, plugin.Host) error { return nil }
func (*pausingPlugin) Shutdown(context.Context) error          { return nil }

func (p *pausingPlugin) OnBeforeRender(_ context.Context, ev *plugin.BeforeRenderEvent) error {
	if ev.Path == p.path {
		p.once.Do(func() {
			close(p.started)
			<-p.release
		})
	}
	return nil
}

// A render that starts while an invalidation is under way — after it began, before
// its marks land — may read the data it is replacing, and it is still running when
// the invalidation ends. It is served, and not stored as fresh.
func TestInvalidateTags_ARenderStartedMidInvalidationIsNotStored(t *testing.T) {
	store := &blockingMarkCache{
		MemoryCache: cache.NewMemory(cache.MemoryConfig{}),
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	pause := &pausingPlugin{path: "/about", started: make(chan struct{}), release: make(chan struct{})}
	app := newTestApp(t, func(cfg *Config) { cfg.Cache.Store = store })
	if err := app.RegisterPlugin(pause); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}
	about := newHomePage()
	about.Name = "about"
	about.Paths = map[string]string{"en": "/about"}
	for _, page := range []*types.Page{newHomePage(), about} {
		if err := app.RegisterPage(page); err != nil {
			t.Fatalf("RegisterPage: %v", err)
		}
	}
	handler := app.Handler()
	if rec := get(handler, "/"); rec.Code != http.StatusOK {
		t.Fatalf("warming status = %d", rec.Code)
	}

	invalidated := make(chan error, 1)
	go func() { invalidated <- app.InvalidateTags(context.Background(), "homepage") }()
	<-store.entered

	served := make(chan *httptest.ResponseRecorder, 1)
	go func() { served <- get(handler, "/about") }()
	<-pause.started
	close(store.release)
	if err := <-invalidated; err != nil {
		t.Fatalf("InvalidateTags: %v", err)
	}
	close(pause.release)
	rec := <-served

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Welcome Home") {
		t.Fatalf("render begun mid-invalidation = %d %q, want it served", rec.Code, rec.Body.String())
	}
	// The home page, kept stale; nothing for /about.
	if n := store.Stats().Entries; n != 1 {
		t.Fatalf("store holds %d entries, want 1: a render begun mid-invalidation was stored", n)
	}
}

// A "disk" cache's memory tier is never larger than MaxBytes: a cap set below the
// tier's own default bounds the process's memory as well as the disk, and a page
// the disk has evicted is not still served from memory.
func TestBuildCache_ADiskCachesMemoryTierHonoursASmallerMaxBytes(t *testing.T) {
	ctx := context.Background()
	var cfg Config
	cfg.Cache.Type = "disk"
	cfg.Cache.Dir = t.TempDir()
	cfg.Cache.Version = "v"
	cfg.Cache.MaxBytes = 4096
	store, err := buildCache(cfg, false, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("buildCache: %v", err)
	}
	tiered, ok := store.(*cache.TieredCache)
	if !ok {
		t.Fatalf("store is %T, want a tiered cache", store)
	}

	page := []byte(strings.Repeat("x", 3000))
	if _, err := tiered.Set(ctx, "first", page, time.Hour); err != nil {
		t.Fatal(err)
	}
	// Older than the second, so the disk evicts it first.
	files, err := os.ReadDir(tiered.Dir())
	if err != nil || len(files) != 1 {
		t.Fatalf("ReadDir = %v, %v, want one file", files, err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(tiered.Dir(), files[0].Name()), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := tiered.Set(ctx, "second", page, time.Hour); err != nil {
		t.Fatal(err)
	}

	if _, _, found := tiered.Get(ctx, "first"); found {
		t.Error("the memory tier still serves a page past MaxBytes")
	}
	if _, _, found := tiered.Get(ctx, "second"); !found {
		t.Error("the newest page is missing")
	}
}
