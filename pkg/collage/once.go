package collage

import (
	"context"
	"time"

	"github.com/Elagoht/collage/internal/datacache"
	"github.com/Elagoht/collage/internal/types"
)

// Once runs fetch at most once per render for a given key, and hands its result to
// every fragment that asks for the same key.
//
//	var articleKey = collage.NewKey[Article]("article")
//
//	article, err := collage.Once(rc, articleKey.With(slug), func(ctx context.Context) (Article, error) {
//		return api.Article(ctx, slug)
//	})
//
// It exists because the obvious way to share work between fragments has a hole in
// it. The usual shape — Get a key, fetch on a miss, Set it back — is a check
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
// A key is its name and its type: NewKey[A]("x") and NewKey[B]("x") are two keys,
// fetched separately, so a fragment cannot be handed a value of a type it did not
// ask for.
func Once[T any](rc *RenderContext, key Key[T], fetch func(context.Context) (T, error)) (T, error) {
	return types.Once(rc, key, fetch)
}

// Cached returns the value stored under key, fetching it when there is none, and
// keeps it across renders — every page that shows author A shares one fetch:
//
//	var authorKey = collage.NewKey[Author]("author")
//
//	author, err := collage.Cached(rc, authorKey.With(id), time.Hour, []string{"author:" + id},
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
// everyone waiting on it and never stored — except a fetch that failed because
// the request that started it went away, which a waiter still being served
// fetches again rather than failing with. Values are kept in this process, bounded
// by Cache.MaxEntries, least recently used first to go.
//
// Where nothing is kept across renders — Cache.Enabled false, development, a
// request that called SkipCache, an action's own handler — it is Once: shared
// within the render, fetched fresh by the next. As with Once, a key is its name and
// its type: two keys of one name and different types are two values.
func Cached[T any](rc *RenderContext, key Key[T], ttl time.Duration, tags []string, fetch func(context.Context) (T, error)) (T, error) {
	return types.Cached(rc, key, ttl, tags, fetch)
}

// ErrCachedFetchPanicked is what a Cached call waiting on another's fetch is
// told when that fetch panicked. The panic goes to the render whose fetch it
// was, and the key is free: the next call fetches again.
var ErrCachedFetchPanicked = datacache.ErrFetchPanicked
