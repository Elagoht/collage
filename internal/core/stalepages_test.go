package core

import (
	"context"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/cache"
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

func TestInvalidationEpoch_AdvancesPerCall(t *testing.T) {
	app, _ := warmedHome(t, nil)
	ctx := context.Background()

	before := app.invalidationEpoch.Load()
	_ = app.InvalidateTags(ctx, "homepage")
	_ = app.PurgeTags(ctx, "homepage")
	if got := app.invalidationEpoch.Load(); got != before+2 {
		t.Fatalf("epoch = %d after two calls, want %d", got, before+2)
	}
	_ = app.InvalidateTags(ctx)
	if got := app.invalidationEpoch.Load(); got != before+2 {
		t.Fatalf("epoch = %d after an empty call, want it unchanged at %d", got, before+2)
	}
}
