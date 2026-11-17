package core

import (
	"testing"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/dependency"
)

// The tracker's total key count is bounded whatever the store is. The host tag
// is exempt from the per-tag cap, so MaxKeys is the only thing standing between
// a client minting distinct query strings against a cacheable page and a tracker
// that grows for as long as the process runs. It used to be set only for the
// built-in store with a positive MaxEntries.
func TestNew_TrackerTotalCapHoldsForACustomStoreAndANegativeMaxEntries(t *testing.T) {
	cases := []struct {
		name       string
		maxEntries int
		custom     bool
		want       int
	}{
		{name: "custom store, default entries", maxEntries: 0, custom: true, want: defaultMaxEntries},
		{name: "custom store, negative entries", maxEntries: -1, custom: true, want: defaultMaxEntries},
		{name: "custom store, positive entries", maxEntries: 7, custom: true, want: 7},
		{name: "built-in store, negative entries", maxEntries: -1, custom: false, want: defaultMaxEntries},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app := newTestApp(t, func(cfg *Config) {
				cfg.Cache.MaxEntries = c.maxEntries
				if c.custom {
					cfg.Cache.Store = cache.NewMemory(cache.MemoryConfig{})
				}
			})
			tracker, ok := app.tracker.(*dependency.MemoryTracker)
			if !ok {
				t.Fatalf("tracker is %T, want *dependency.MemoryTracker", app.tracker)
			}
			if tracker.MaxKeys != c.want {
				t.Fatalf("MaxKeys = %d, want %d", tracker.MaxKeys, c.want)
			}
		})
	}
}
