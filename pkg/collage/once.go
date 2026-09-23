package collage

import (
	"context"

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
