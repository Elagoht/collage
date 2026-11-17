package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/types"
)

// cancelDuringRender is an engine that ends the leader's request while the
// render is under way, the way a reader closing the tab does. The render itself
// is detached from the request and carries on.
type cancelDuringRender struct {
	inner  *fakeEngine
	cancel context.CancelFunc
}

func (c *cancelDuringRender) RenderFragment(ctx context.Context, rc *types.RenderContext, f *types.Fragment) ([]byte, error) {
	return c.inner.RenderFragment(ctx, rc, f)
}

func (c *cancelDuringRender) Render(ctx context.Context, rc *types.RenderContext) (*render.Result, error) {
	c.cancel()
	return c.inner.Render(ctx, rc)
}

// ctxBlindCache is a Cache with no tagging of its own, hiding MemoryCache's other
// methods, and one that ignores the context as a custom store may.
type ctxBlindCache struct{ cache.Cache }

func (p ctxBlindCache) Set(_ context.Context, key string, content []byte, ttl time.Duration) (string, error) {
	return p.Cache.Set(context.Background(), key, content, ttl)
}

func (p ctxBlindCache) Get(_ context.Context, key string) ([]byte, string, bool) {
	return p.Cache.Get(context.Background(), key)
}

// A leader that disconnects mid-render must not take the cache write with it:
// the render is shared, and the entry is for the readers after it.
func TestHandler_LeaderLeavingMidRenderStillCachesThePage(t *testing.T) {
	page := testPage("home", "/", types.StrategyIncremental)
	inner := newFakeEngine(fakeRender{html: "<html>home</html>", tags: []string{"post:1"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := newEnv(t, []*types.Page{page}, func(d *Deps) {
		d.Renderer = &cancelDuringRender{inner: inner, cancel: cancel}
	})

	env.do(httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx))

	key := flightKeyFor(t, env, "/")
	if _, _, found := env.cache.Get(context.Background(), key); !found {
		t.Fatal("the page the leader rendered was not cached: the write ran under its cancelled request")
	}
	tags, err := env.tracker.Tags(context.Background(), key)
	if err != nil || len(tags) == 0 {
		t.Fatalf("tracker.Tags = %v, %v, want the page tracked", tags, err)
	}
}

// With a cache that ignores the context the entry is stored even under a
// cancelled one, so the tracker must still learn of it: an entry no tag reaches
// would outlive every invalidation, forever for a Static page.
func TestHandler_LeaderLeavingMidRenderStillTracksAnEntryACustomCacheStored(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	inner := newFakeEngine(fakeRender{html: "<html>home</html>", tags: []string{"post:1"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := newEnv(t, []*types.Page{page}, func(d *Deps) {
		d.Renderer = &cancelDuringRender{inner: inner, cancel: cancel}
		d.Cache = ctxBlindCache{cache.NewMemory(cache.MemoryConfig{})}
	})

	env.do(httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx))

	key := flightKeyFor(t, env, "/")
	keys, err := env.tracker.Resolve(context.Background(), []string{"post:1"})
	if err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	found := false
	for _, k := range keys {
		found = found || k == key
	}
	if !found {
		t.Fatalf("Resolve(post:1) = %v, want %q: the entry is stored but no invalidation can reach it", keys, key)
	}
}
