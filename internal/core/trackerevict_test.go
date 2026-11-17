package core

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/observability"
)

// plainStore is a cache.Cache with no tags and no stale marks of its own, as a
// simple Redis-backed store would be, so the dependency tracker is the only thing
// that can find an entry by tag.
type plainStore struct{ inner *cache.MemoryCache }

func (p *plainStore) Get(ctx context.Context, key string) ([]byte, string, bool) {
	return p.inner.Get(ctx, key)
}
func (p *plainStore) Set(ctx context.Context, key string, content []byte, ttl time.Duration) (string, error) {
	return p.inner.Set(ctx, key, content, ttl)
}
func (p *plainStore) InvalidateKey(ctx context.Context, key string) error {
	return p.inner.InvalidateKey(ctx, key)
}
func (p *plainStore) Clear(ctx context.Context) error { return p.inner.Clear(ctx) }

// A key the tracker's cap dropped must not be served afterwards as fresh. The
// store does not know the cap exists, so without the eviction callback it keeps
// the entry, an invalidation of its tag no longer reaches it, and a Static page
// is stale until its TTL.
func TestApp_AKeyEvictedFromTheTrackerIsNotServedAfterwards(t *testing.T) {
	// The built-in store's own eviction order need not match the tracker's, so it
	// is held to the same guarantee.
	for _, custom := range []bool{true, false} {
		name := "built-in store"
		if custom {
			name = "custom plain store"
		}
		t.Run(name, func(t *testing.T) { evictedKeyIsNotServed(t, custom) })
	}
}

func evictedKeyIsNotServed(t *testing.T, custom bool) {
	metrics := observability.NewRecordingMetrics()
	app := newTestApp(t, func(cfg *Config) {
		cfg.Observability.Metrics = metrics
		cfg.Cache.MaxEntries = 3
		cfg.Cache.WarmLimit = -1
		if custom {
			cfg.Cache.Store = &plainStore{inner: cache.NewMemory(cache.MemoryConfig{})}
		}
	})
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	handler := app.Handler()

	const variants = 6
	for i := range variants {
		get(handler, fmt.Sprintf("/?q=%d", i))
	}
	if fresh, _ := renderCounts(metrics); fresh != variants {
		t.Fatalf("fresh renders = %d, want %d", fresh, variants)
	}

	if err := app.InvalidateTags(context.Background(), "homepage"); err != nil {
		t.Fatalf("InvalidateTags: %v", err)
	}

	// q=0 was the first written, so the first the cap dropped.
	get(handler, "/?q=0")
	if fresh, hits := renderCounts(metrics); fresh != variants+1 || hits != 0 {
		t.Fatalf("after invalidation fresh=%d hits=%d, want %d and 0: the evicted key was served stale",
			fresh, hits, variants+1)
	}
}
