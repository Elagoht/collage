package core

import (
	"context"
	"testing"

	"github.com/Elagoht/collage/internal/observability"
)

// An invalidation made under a cancelled context — an action's result for a
// request whose client has gone, or a webhook handler passing r.Context() — must
// still drop the pages. The tracker and the memory cache refuse a done context,
// so run as-is it resolved nothing and reported nothing, and the stale page
// stayed for its whole TTL.
func TestApp_InvalidateTagsUnderACancelledContextStillInvalidates(t *testing.T) {
	metrics := observability.NewRecordingMetrics()
	app := newTestApp(t, func(cfg *Config) {
		cfg.Observability.Metrics = metrics
	})
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	handler := app.Handler()
	get(handler, "/")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	invalidated, err := app.InvalidateTagsN(ctx, "homepage")
	if err != nil {
		t.Fatalf("InvalidateTagsN: %v", err)
	}
	if invalidated != 1 {
		t.Fatalf("InvalidateTagsN = %d, want 1: a cancelled caller invalidated nothing", invalidated)
	}

	get(handler, "/")
	if fresh, _ := renderCounts(metrics); fresh != 2 {
		t.Fatalf("fresh renders = %d, want 2: the page was still served from cache", fresh)
	}
}
