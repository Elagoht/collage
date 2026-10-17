package core

import (
	"context"
	"net/http"
	"testing"

	"github.com/Elagoht/collage/internal/httpx"
)

// warmingHome is warmedHome with warming on, which testConfig turns off.
func warmingHome(t *testing.T) (*App, string) {
	t.Helper()
	app, key := warmedHomeWith(t, func(cfg *Config) { cfg.Cache.WarmLimit = 0 })
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	return app, key
}

// The keys an invalidation reached are warmed once it has finished, so the warm
// render is stored: the next request is a HIT, with no render of its own.
func TestWarmer_AnInvalidationWarmsWhatItReached(t *testing.T) {
	for _, purge := range []bool{false, true} {
		app, _ := warmingHome(t)
		var err error
		if purge {
			err = app.PurgeTags(context.Background(), "homepage")
		} else {
			err = app.InvalidateTags(context.Background(), "homepage")
		}
		if err != nil {
			t.Fatal(err)
		}
		handler := app.Handler().(*httpx.Handler)
		handler.WaitWarmed()
		rec := get(handler, "/")
		if rec.Code != http.StatusOK || rec.Header().Get(httpx.CacheStatusHeader) != "HIT" {
			t.Fatalf("purge %v: after the warm = %d %s=%q, want a HIT", purge, rec.Code,
				httpx.CacheStatusHeader, rec.Header().Get(httpx.CacheStatusHeader))
		}
	}
}

// Shutdown stops the warmer, so an invalidation after it warms nothing.
func TestWarmer_StoppedByShutdown(t *testing.T) {
	app, key := warmingHome(t)
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := app.InvalidateTags(context.Background(), "homepage"); err != nil {
		t.Fatal(err)
	}
	app.Handler().(*httpx.Handler).WaitWarmed()
	if _, _, found := app.store.Get(context.Background(), key); found {
		t.Fatal("the page is fresh after an invalidation past Shutdown: it was warmed")
	}
}
