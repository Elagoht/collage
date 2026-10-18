package core

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/types"
)

type sharedDataValue string

func (v sharedDataValue) Encode() ([]byte, error) { return json.Marshal(string(v)) }

// A Store a fleet shares holds collage.Cached's values too, even where Type and
// Dir would otherwise put them on disk: a per-instance copy is what it ends.
func TestBuildDataCache_ASharedStoreHoldsTheData(t *testing.T) {
	shared := cache.NewSharedMemory()
	dir := t.TempDir()
	cfg := Config{}
	cfg.Cache.Store = shared
	cfg.Cache.Type = "disk"
	cfg.Cache.Dir = dir
	store := buildDataCache(cfg, slog.New(slog.DiscardHandler), observability.MetricsOrNoop(nil), nil)
	t.Cleanup(func() { store.Close(context.Background()) })

	fetch := func(context.Context) (types.CacheValue, error) { return sharedDataValue("v"), nil }
	decode := func(data []byte) (types.CacheValue, error) {
		var v string
		err := json.Unmarshal(data, &v)
		return sharedDataValue(v), err
	}
	if _, _, err := store.Load(context.Background(), "k", 0, nil, fetch, decode); err != nil {
		t.Fatal(err)
	}
	if shared.Data() != 1 || store.Len() != 0 {
		t.Errorf("shared holds %d, store %d; want the value in the shared store only", shared.Data(), store.Len())
	}
	if _, err := os.Stat(filepath.Join(dir, "data")); !os.IsNotExist(err) {
		t.Errorf("data directory created (%v); want none beside a shared store", err)
	}
}
