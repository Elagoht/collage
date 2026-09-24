package types

import (
	"context"
	"errors"
	"time"
)

// DataCache is what Cached stores values in across renders. internal/datacache is
// the implementation; the interface is here so a RenderContext can carry one
// without this package depending on it.
type DataCache interface {
	Load(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (any, error)) (any, error) // any: the store holds every caller's type
}

// ErrCachedTypeMismatch reports one Cached key asked for as two different types.
var ErrCachedTypeMismatch = errors.New("collage: Cached key holds a value of a different type")

// BindDataCache gives rc — and every copy of it the render makes — the store behind
// Cached. The render engine calls it.
func BindDataCache(rc *RenderContext, store DataCache) {
	if rc != nil && rc.state != nil {
		rc.state.mu.Lock()
		rc.state.data = store
		rc.state.mu.Unlock()
	}
}

// SkipDataCache makes Cached fetch fresh for this render: neither serving a stored
// value nor storing the one it fetched. It is how a preview sees a draft.
func SkipDataCache(rc *RenderContext) {
	if rc != nil && rc.state != nil {
		rc.state.mu.Lock()
		rc.state.uncached = true
		rc.state.mu.Unlock()
	}
}

// DeclaredTags returns the dependency tags this render's Cached calls declared.
func DeclaredTags(rc *RenderContext) []string {
	if rc == nil || rc.state == nil {
		return nil
	}
	rc.state.mu.Lock()
	defer rc.state.mu.Unlock()
	return append([]string(nil), rc.state.tags...)
}

// Cached returns the value stored under key, fetching it when there is none. Unlike
// Once, what it stores outlives the render: every page that asks for "author:A"
// shares one fetch until ttl passes or one of tags is invalidated.
//
// tags are also added to the render's own dependency tags, so a page built from
// the value is invalidated with it — one InvalidateTags("author:A") drops the
// author and every page showing them.
//
// Where there is no store to keep it in — development, a preview, a render the
// application built by hand, or a cache that is not enabled — it is Once: shared
// within the render, fetched fresh by the next.
func Cached[T any](rc *RenderContext, key string, ttl time.Duration, tags []string, fetch func(context.Context) (T, error)) (T, error) {
	var zero T
	if rc == nil || rc.state == nil {
		if fetch == nil {
			return zero, nil
		}
		return fetch(context.Background())
	}

	rc.state.mu.Lock()
	rc.state.tags = append(rc.state.tags, tags...)
	store, uncached := rc.state.data, rc.state.uncached
	rc.state.mu.Unlock()

	if store == nil || uncached {
		// Namespaced, so a Cached key and a Once key the application happens to
		// spell alike are not one entry holding two types.
		return Once(rc, "collage:cached:"+key, fetch)
	}

	value, err := store.Load(rc.Context(), key, ttl, tags, func(ctx context.Context) (any, error) { // any: see DataCache
		return fetch(ctx)
	})
	if err != nil {
		return zero, err
	}
	typed, ok := value.(T)
	if !ok {
		return zero, ErrCachedTypeMismatch
	}
	return typed, nil
}
