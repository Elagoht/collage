package types

import (
	"context"
	"fmt"
	"slices"
	"strings"
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

// A key is its name and its type: one name asked for as two types is two
// values, in the store and in the Once it falls back to. Both calls' tags are
// the render's.
func TestCached_SameNameDifferentTypes(t *testing.T) {
	for _, skip := range []bool{false, true} {
		rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
		store := mapCache{}
		BindDataCache(rc, store)
		if skip {
			SkipDataCache(rc)
		}
		s, err := Cached(rc, NewKey[string]("x"), time.Hour, []string{"t1"}, func(context.Context) (string, error) { return "s", nil })
		n, err2 := Cached(rc, NewKey[int]("x"), time.Hour, []string{"t2"}, func(context.Context) (int, error) { return 7, nil })
		if err != nil || err2 != nil || s != "s" || n != 7 {
			t.Errorf("skip=%v: %q %v / %d %v", skip, s, err, n, err2)
		}
		if tags := DeclaredTags(rc); !slices.Equal(tags, []string{"t1", "t2"}) {
			t.Errorf("skip=%v: DeclaredTags = %v, want both calls' tags", skip, tags)
		}
		if want := 2; !skip && len(store) != want {
			t.Errorf("store holds %d entries, want %d", len(store), want)
		}
	}
}

// A nil stored under an interface-typed key reads back as nil, not as an error.
func TestCached_NilUnderAnInterfaceKey(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	store := mapCache{}
	BindDataCache(rc, store)
	key := NewKey[fmt.Stringer]("s")
	calls := 0
	fetch := func(context.Context) (fmt.Stringer, error) { calls++; return nil, nil }
	for i := range 2 {
		v, err := Cached(rc, key, 0, nil, fetch)
		if err != nil || v != nil {
			t.Errorf("call %d = %v, %v; want nil, nil", i, v, err)
		}
	}
	if calls != 1 {
		t.Errorf("fetched %d times, want once: the stored nil was not found", calls)
	}
}

// wrongStore answers every Load with a value of its own choosing.
type wrongStore struct{ value any } // any: mirrors DataCache's own

func (w wrongStore) Load(context.Context, string, time.Duration, []string, func(context.Context) (any, error)) (any, error) { // any: see DataCache
	return w.value, nil
}

// A store handing back a value of another type is a bug in the store; Cached
// says so rather than returning an empty value as if all were well.
func TestCached_AStoreReturningAnotherTypeIsAnError(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	BindDataCache(rc, wrongStore{value: "not an int"})
	v, err := Cached(rc, NewKey[int]("k"), 0, nil, func(context.Context) (int, error) { return 7, nil })
	if err == nil {
		t.Fatalf("Cached = %d, nil; want an error naming the store's mistake", v)
	}
	if !strings.Contains(err.Error(), `"k"`) {
		t.Errorf("error %q does not name the key", err)
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
		Cached(rc, NewKey[int]("k"), 0, nil, fetch)
		Cached(rc, NewKey[int]("k"), 0, nil, fetch)
		if calls != 1 {
			t.Errorf("skip=%v: fetched %d times in one render, want once", skip, calls)
		}
		if len(store) != 0 {
			t.Errorf("skip=%v: the store was written", skip)
		}
	}
}
