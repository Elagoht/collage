package types

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// mapCache is a DataCache that keeps everything forever.
type mapCache map[string]any // any: mirrors DataCache's own

func (m mapCache) Load(ctx context.Context, key string, _ time.Duration, _ []string, fetch func(context.Context) (any, error)) (any, error) { // any: see DataCache
	if v, ok := m[key]; ok {
		return v, nil
	}
	v, err := fetch(ctx)
	if err == nil {
		m[key] = v
	}
	return v, err
}

func TestCached_TypeMismatchAndDeclaredTags(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	BindDataCache(rc, mapCache{})

	if v, err := Cached(rc, "k", 0, []string{"t1"}, func(context.Context) (int, error) { return 7, nil }); err != nil || v != 7 {
		t.Fatalf("Cached = %d, %v", v, err)
	}
	if _, err := Cached(rc, "k", 0, []string{"t2"}, func(context.Context) (string, error) { return "", nil }); !errors.Is(err, ErrCachedTypeMismatch) {
		t.Errorf("a key read as another type = %v, want ErrCachedTypeMismatch", err)
	}
	if tags := DeclaredTags(rc); !slices.Equal(tags, []string{"t1", "t2"}) {
		t.Errorf("DeclaredTags = %v, want both calls' tags", tags)
	}
}

// With no store, or skipping it, Cached is Once: shared within the render only.
func TestCached_WithoutAStoreIsOnce(t *testing.T) {
	for _, skip := range []bool{false, true} {
		rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
		store := mapCache{}
		if skip {
			BindDataCache(rc, store)
			SkipDataCache(rc)
		}
		calls := 0
		fetch := func(context.Context) (int, error) { calls++; return calls, nil }
		Cached(rc, "k", 0, nil, fetch)
		Cached(rc, "k", 0, nil, fetch)
		if calls != 1 {
			t.Errorf("skip=%v: fetched %d times in one render, want once", skip, calls)
		}
		if len(store) != 0 {
			t.Errorf("skip=%v: the store was written", skip)
		}
	}
}
