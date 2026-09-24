package collage

import (
	"context"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

// Once runs fetch at most once per render for a given key, and hands its result to
// every fragment that asks for the same key.
//
//	article, err := collage.Once(rc, "article:"+slug, func(ctx context.Context) (Article, error) {
//		return api.Article(ctx, slug)
//	})
//
// It exists because the obvious way to share work between fragments has a hole in
// it. The usual shape — read SharedData, fetch on a miss, write it back — is a check
// and then an act, with the fetch in between. Sibling fragments' data handlers run
// concurrently, so two of them can both miss and both make the same call: the page
// renders correctly and quietly asks the upstream twice for one article.
//
// The first caller for a key fetches; the rest wait and receive what it produced,
// errors included. A failed fetch is a result: retrying it once per fragment is how
// one slow failure becomes several.
//
// The result lives exactly as long as the render, so there is no eviction policy and
// nothing to configure. What should outlive a request belongs in the page cache.
//
// Keys are the application's own namespace. Two fragments asking for one key as two
// different types get ErrOnceTypeMismatch rather than a silently empty section.
func Once[T any](rc *RenderContext, key string, fetch func(context.Context) (T, error)) (T, error) {
	return types.Once(rc, key, fetch)
}

// ErrOnceTypeMismatch reports that one Once key was asked for as two different types
// within a single render.
var ErrOnceTypeMismatch = types.ErrOnceTypeMismatch

// Get reads the value a fragment stored under key with rc.Set, as the type it was
// stored as:
//
//	post, ok := collage.Get[Post](rc, "post")
//
// ok is false when nothing is stored under key, and when what is stored is not a T
// — the same answer, because to the caller both mean the value it wanted is not
// there. It replaces the type assertion every read of shared data otherwise needs.
func Get[T any](rc *RenderContext, key string) (T, bool) {
	stored, ok := rc.Get(key)
	if !ok {
		var zero T
		return zero, false
	}
	value, ok := stored.(T)
	return value, ok
}

// Cached returns the value stored under key, fetching it when there is none, and
// keeps it across renders — every page that shows author A shares one fetch:
//
//	author, err := collage.Cached(rc, "author:"+id, time.Hour, []string{"author:" + id},
//		func(ctx context.Context) (Author, error) { return api.Author(ctx, id) })
//
// Once shares work between the fragments of one page; Cached shares it between
// pages, and between requests. A static export of thirty pages by two authors asks
// for the authors twice rather than thirty times.
//
// tags do two things. They are added to the page's own dependency tags, so a
// cached page built from the value is invalidated with it, and InvalidateTags
// drops the stored value too: one call replaces an author and every page showing
// them, with no second cache to keep in step. ttl bounds how long a value is kept
// when nothing invalidates it; zero keeps it until something does.
//
// Concurrent requests for one key share one fetch. An error is returned to
// everyone waiting on it and never stored. Values are kept in this process, bounded
// by Cache.MaxEntries, least recently used first to go.
//
// Where nothing is kept across renders — Cache.Enabled false, development, a
// request that called SkipCache, an action's own handler — it is Once: shared
// within the render, fetched fresh by the next. Two calls asking for one key as
// two types get ErrCachedTypeMismatch.
func Cached[T any](rc *RenderContext, key string, ttl time.Duration, tags []string, fetch func(context.Context) (T, error)) (T, error) {
	return types.Cached(rc, key, ttl, tags, fetch)
}

// ErrCachedTypeMismatch reports one Cached key asked for as two different types.
var ErrCachedTypeMismatch = types.ErrCachedTypeMismatch
