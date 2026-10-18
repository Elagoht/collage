package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/dependency"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/types"
)

// sharedSpy is a SharedMemory that records each SetSince, and can fail Epoch:
// what a test asserts the handler sent a shared store, and a store that is down.
type sharedSpy struct {
	*cache.SharedMemory
	epochErr error

	mu     sync.Mutex
	writes []sharedWrite
}

// sharedWrite is one SetSince the spy saw.
type sharedWrite struct {
	key   string
	tags  []string
	meta  []byte
	since uint64
}

var _ cache.SharedCache = (*sharedSpy)(nil)

func newSharedSpy() *sharedSpy {
	return &sharedSpy{SharedMemory: cache.NewSharedMemory()}
}

func (s *sharedSpy) Epoch(ctx context.Context) (uint64, error) {
	if s.epochErr != nil {
		return 0, s.epochErr
	}
	return s.SharedMemory.Epoch(ctx)
}

func (s *sharedSpy) SetSince(ctx context.Context, key string, content []byte, ttl time.Duration, tags []string, meta []byte, since uint64) (string, cache.WriteOutcome, error) {
	s.mu.Lock()
	s.writes = append(s.writes, sharedWrite{key: key, tags: slices.Clone(tags), meta: slices.Clone(meta), since: since})
	s.mu.Unlock()
	return s.SharedMemory.SetSince(ctx, key, content, ttl, tags, meta, since)
}

func (s *sharedSpy) seen() []sharedWrite {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.writes)
}

// withSharedCache makes store the handler's cache.
func withSharedCache(store cache.SharedCache) envOption {
	return func(d *Deps) { d.Cache = store }
}

// A page written to a shared store goes through SetSince, carrying the route's
// path and host tags in the store's own index and meta naming where it lives,
// so any instance can purge or re-render it.
func TestShared_WriteCarriesMetaAndRouteTags(t *testing.T) {
	store := newSharedSpy()
	page := testPage("home", "/", types.StrategyIncremental)
	env := newEnv(t, []*types.Page{page}, withSharedCache(store))
	env.engine.set("home", fakeRender{html: "<html>home</html>", tags: []string{"posts"}})

	if rec := env.get("/"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	writes := store.seen()
	if len(writes) != 1 {
		t.Fatalf("SetSince calls = %d, want 1", len(writes))
	}
	w := writes[0]
	for _, want := range []string{"posts", types.PathTag("/"), types.HostTag("example.com")} {
		if !slices.Contains(w.tags, want) {
			t.Errorf("tags = %q, want %q among them", w.tags, want)
		}
	}
	host, path, ok := DecodeSharedMeta(w.meta)
	if !ok || host != "example.com" || path != "/" {
		t.Errorf("DecodeSharedMeta = %q %q %v, want example.com / true", host, path, ok)
	}
	if _, _, found := store.Get(context.Background(), w.key); !found {
		t.Errorf("entry %q not stored fresh", w.key)
	}
	if got := env.get("/").Header().Get(CacheStatusHeader); got != cacheHit {
		t.Errorf("next request %s = %q, want HIT", CacheStatusHeader, got)
	}
}

// A render that another instance's invalidation overlapped — one that reached
// the store while the render ran, with no trace in this process — is served,
// and lands stale: still the best copy to fall back on, never served as fresh.
func TestShared_OverlappedRenderLandsStale(t *testing.T) {
	store := cache.NewSharedMemory()
	page := testPage("home", "/", types.StrategyIncremental)
	inner := newFakeEngine(fakeRender{html: "<html>home</html>", tags: []string{"posts"}})
	blocking := newBlockingEngine(inner)
	env := newEnv(t, []*types.Page{page}, withSharedCache(store), func(d *Deps) { d.Renderer = blocking })

	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- env.get("/") }()
	<-blocking.started
	if _, err := store.DropTags(context.Background(), []string{"posts"}, false); err != nil {
		t.Fatal(err)
	}
	close(blocking.release)
	rec := <-done

	if rec.Code != http.StatusOK || rec.Body.String() != "<html>home</html>" {
		t.Fatalf("overlapped render = %d %q, want it served", rec.Code, rec.Body.String())
	}
	key := flightKeyFor(t, env, "/")
	content, _, stale, found := store.GetStale(context.Background(), key)
	if !found || !stale || string(content) != "<html>home</html>" {
		t.Errorf("GetStale = %q stale=%v found=%v, want the render kept stale", content, stale, found)
	}
	if _, _, found := store.Get(context.Background(), key); found {
		t.Error("Get found the overlapped render fresh")
	}
}

// A purge that overlapped the render leaves nothing stored.
func TestShared_OverlappedPurgeIsNotStored(t *testing.T) {
	store := cache.NewSharedMemory()
	page := testPage("home", "/", types.StrategyIncremental)
	inner := newFakeEngine(fakeRender{html: "<html>home</html>", tags: []string{"posts"}})
	blocking := newBlockingEngine(inner)
	env := newEnv(t, []*types.Page{page}, withSharedCache(store), func(d *Deps) { d.Renderer = blocking })

	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- env.get("/") }()
	<-blocking.started
	if _, err := store.DropTags(context.Background(), []string{"posts"}, true); err != nil {
		t.Fatal(err)
	}
	close(blocking.release)
	if rec := <-done; rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if n := store.Pages(); n != 0 {
		t.Errorf("store holds %d pages, want none after an overlapping purge", n)
	}
}

// A store whose epoch cannot be read cannot weigh the write, so none is made:
// the reader still gets the page, and nothing is reported.
func TestShared_EpochErrorSkipsWrite(t *testing.T) {
	store := newSharedSpy()
	store.epochErr = errors.New("redis down")
	page := testPage("home", "/", types.StrategyIncremental)
	env := newEnv(t, []*types.Page{page}, withSharedCache(store))

	rec := env.get("/")
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>page</html>" {
		t.Fatalf("response = %d %q, want the page", rec.Code, rec.Body.String())
	}
	if n := len(store.seen()); n != 0 {
		t.Errorf("SetSince calls = %d, want none", n)
	}
	if n := store.Pages(); n != 0 {
		t.Errorf("store holds %d pages, want none", n)
	}
	if n := levelCount(env.logs, slog.LevelError); n != 0 {
		t.Errorf("error records = %d, want none: an unreadable epoch is a skipped write", n)
	}
}

// A document written to a shared store goes through SetSince too, and an
// invalidation overlapping its handler lands it stale.
func TestShared_DocumentWriteWeighsTheStore(t *testing.T) {
	store := newSharedSpy()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	doc := &types.Document{
		Name: "sitemap", ContentType: "application/xml",
		Paths:    map[string]string{"en": "/sitemap.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			once.Do(func() { close(started) })
			<-release
			return []byte("<urlset/>"), []string{"blog:posts"}, nil
		},
	}
	rt := router.New(router.LocaleOptions{Default: "en"})
	if err := rt.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	h, err := New(Deps{
		Router:   rt,
		Renderer: render.New(nil, render.Options{}),
		Cache:    store,
		Tracker:  dependency.NewMemory(),
		Logger:   slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan *httptest.ResponseRecorder)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil))
		done <- rec
	}()
	<-started
	if _, err := store.DropTags(context.Background(), []string{"blog:posts"}, false); err != nil {
		t.Fatal(err)
	}
	close(release)
	if rec := <-done; rec.Code != http.StatusOK || rec.Body.String() != "<urlset/>" {
		t.Fatalf("response = %d %q, want the document", rec.Code, rec.Body.String())
	}

	writes := store.seen()
	if len(writes) != 1 {
		t.Fatalf("SetSince calls = %d, want 1", len(writes))
	}
	w := writes[0]
	if !slices.Contains(w.tags, "blog:posts") || !slices.Contains(w.tags, types.PathTag("/sitemap.xml")) {
		t.Errorf("tags = %q, want the document's and its path tag", w.tags)
	}
	if host, path, ok := DecodeSharedMeta(w.meta); !ok || host != "example.com" || path != "/sitemap.xml" {
		t.Errorf("DecodeSharedMeta = %q %q %v", host, path, ok)
	}
	if _, _, stale, found := store.GetStale(context.Background(), w.key); !found || !stale {
		t.Errorf("GetStale stale=%v found=%v, want the document kept stale", stale, found)
	}
}

// A recipe another instance wrote survives the meta: adopted, it rebuilds the
// same request — URL, headers and vary set — as the one it was made from.
func TestShared_RecipeRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cacheParams []string
	}{
		{"nil cacheParams", nil},
		{"empty cacheParams", []string{}},
		{"named cacheParams", []string{"page"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := recipe{
				host:        "Example.com:8443",
				path:        "/blog/a b",
				rawPath:     "/blog/a%20b",
				query:       "page=2",
				vary:        []string{"Accept-Language=tr", "X-Theme=dark"},
				header:      http.Header{"Accept-Language": {"tr-TR", "tr"}, "X-Theme": {"dark"}},
				tls:         true,
				document:    true,
				cacheParams: tc.cacheParams,
			}
			env := newEnv(t, nil, withSharedCache(cache.NewSharedMemory()))
			meta := encodeSharedMeta("example.com", "/blog/a b", original)

			held := env.handler.AdoptShared([]cache.Reached{
				{Key: "k1", Meta: meta},
				{Key: "bad", Meta: []byte("not json")},
			})
			if !slices.Equal(held, []string{"k1"}) {
				t.Fatalf("AdoptShared = %q, want [k1]", held)
			}
			got, ok := env.handler.recipes.get("k1")
			if !ok {
				t.Fatal("recipe not adopted")
			}
			if got.document != original.document || got.tls != original.tls {
				t.Errorf("document/tls = %v/%v, want %v/%v", got.document, got.tls, original.document, original.tls)
			}
			if (got.cacheParams == nil) != (original.cacheParams == nil) || !slices.Equal(got.cacheParams, original.cacheParams) {
				t.Errorf("cacheParams = %#v, want %#v", got.cacheParams, original.cacheParams)
			}
			if !env.handler.recipes.readSinceWarm("k1") {
				t.Error("an adopted recipe is not marked read")
			}

			want, have := original.request(context.Background()), got.request(context.Background())
			if want.URL.String() != have.URL.String() || want.RequestURI != have.RequestURI || want.Host != have.Host {
				t.Errorf("URL = %q %q %q, want %q %q %q", have.URL, have.RequestURI, have.Host, want.URL, want.RequestURI, want.Host)
			}
			if (want.TLS == nil) != (have.TLS == nil) {
				t.Errorf("TLS = %v, want %v", have.TLS != nil, want.TLS != nil)
			}
			for name, values := range want.Header {
				if !slices.Equal(have.Header[name], values) {
					t.Errorf("header %s = %q, want %q", name, have.Header[name], values)
				}
			}
			if len(have.Header) != len(want.Header) {
				t.Errorf("headers = %v, want %v", have.Header, want.Header)
			}
			if !slices.Equal(requestVary(have), requestVary(want)) {
				t.Errorf("vary = %q, want %q", requestVary(have), requestVary(want))
			}
		})
	}
}

// A recipe this instance already holds is not replaced by the store's: its own
// is at least as fresh, and its read mark is its own.
func TestShared_AdoptKeepsAHeldRecipe(t *testing.T) {
	env := newEnv(t, nil, withSharedCache(cache.NewSharedMemory()))
	mine := recipe{host: "example.com", path: "/mine"}
	env.handler.recipes.put("k", mine, false)
	env.handler.recipes.clearRead("k")

	theirs := encodeSharedMeta("example.com", "/theirs", recipe{host: "example.com", path: "/theirs"})
	if held := env.handler.AdoptShared([]cache.Reached{{Key: "k", Meta: theirs}}); !slices.Equal(held, []string{"k"}) {
		t.Fatalf("AdoptShared = %q, want [k]", held)
	}
	got, _ := env.handler.recipes.get("k")
	if got.path != "/mine" {
		t.Errorf("recipe path = %q, want the held /mine", got.path)
	}
	if env.handler.recipes.readSinceWarm("k") {
		t.Error("adopting over a held recipe changed its read mark")
	}
}

// failingSetSince is a SharedMemory whose SetSince fails.
type failingSetSince struct{ *cache.SharedMemory }

func (f failingSetSince) SetSince(context.Context, string, []byte, time.Duration, []string, []byte, uint64) (string, cache.WriteOutcome, error) {
	return "", cache.WriteSkipped, errors.New("redis down")
}

// A write the store refuses with an error is reported as any cache write
// failure is, and the reader still gets the page.
func TestShared_SetSinceErrorIsReported(t *testing.T) {
	page := testPage("home", "/", types.StrategyIncremental)
	env := newEnv(t, []*types.Page{page}, withSharedCache(failingSetSince{cache.NewSharedMemory()}))
	if rec := env.get("/"); rec.Code != http.StatusOK || rec.Body.String() != "<html>page</html>" {
		t.Fatalf("response = %d %q, want the page", rec.Code, rec.Body.String())
	}
	if n := levelCount(env.logs, slog.LevelError); n != 1 {
		t.Errorf("error records = %d, want the cache write failure reported once", n)
	}
}

// An invalidation in this process that overlapped the render, but never
// reached the store's epoch, still keeps the render from being fresh: it lands
// stale, as a shared overlap does, rather than dropped.
func TestShared_LocalOverlapLandsStale(t *testing.T) {
	store := cache.NewSharedMemory()
	page := testPage("home", "/", types.StrategyIncremental)
	inner := newFakeEngine(fakeRender{html: "<html>home</html>"})
	blocking := newBlockingEngine(inner)
	var epoch atomic.Uint64
	env := newEnv(t, []*types.Page{page}, withSharedCache(store), withEpoch(&epoch),
		func(d *Deps) { d.Renderer = blocking })

	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- env.get("/") }()
	<-blocking.started
	epoch.Add(1)
	close(blocking.release)
	if rec := <-done; rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	key := flightKeyFor(t, env, "/")
	if _, _, stale, found := store.GetStale(context.Background(), key); !found || !stale {
		t.Errorf("GetStale stale=%v found=%v, want the render kept stale", stale, found)
	}
}
