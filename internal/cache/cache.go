// Package cache defines the render-output cache used by the HTTP handler: an
// interface, a collision-resistant key scheme, and an in-memory implementation.
package cache

import (
	"context"
	"time"
)

// Cache stores rendered pages keyed by request identity and tagged for
// invalidation. An implementation's only defined failure mode is context
// cancellation, reported via the standard context.Canceled and
// context.DeadlineExceeded errors — this package defines no cache-specific error
// sentinels.
type Cache interface {
	// Get returns the cached content and its ETag for key. found is false when
	// there is no fresh entry for key, including one that has expired or, in a
	// StaleCache, been marked stale. Callers must
	// not mutate the returned content slice: an implementation may return its
	// internal copy directly for performance.
	Get(ctx context.Context, key string) (content []byte, etag string, found bool)
	// Set stores content under key with the given ttl, returning content's ETag. A
	// ttl of zero or less uses the cache's configured default TTL. Set takes no
	// tags: an implementation that can associate tags with an entry exposes that
	// through TaggedCache instead.
	Set(ctx context.Context, key string, content []byte, ttl time.Duration) (etag string, err error)
	// InvalidateKey removes the entry stored under key, if any. Invalidating a
	// key that has no entry is not an error.
	InvalidateKey(ctx context.Context, key string) error
	// Clear removes every entry from the cache.
	Clear(ctx context.Context) error
}

// TaggedCache is implemented by caches that can associate tags with an entry at
// write time. Callers type-assert for it; a cache that does not implement it relies
// on the dependency tracker for invalidation.
type TaggedCache interface {
	Cache
	// SetTagged stores content under key with the given ttl and tags, returning
	// content's ETag. A ttl of zero or less uses the cache's configured default
	// TTL. Passing tags associates key with each of them for a later Invalidate.
	SetTagged(ctx context.Context, key string, content []byte, ttl time.Duration, tags []string) (etag string, err error)
	// Invalidate removes every entry associated with any of tags.
	Invalidate(ctx context.Context, tags []string) error
}

// StaleCache is implemented by caches that keep an entry past its invalidation or
// expiry, so a page whose re-render fails can still be answered with the last copy
// that rendered. Callers type-assert for it; a cache that does not implement it
// drops an entry on invalidation and reports an expired one missing, as before.
// There is no maximum stale age: eviction is what bounds a stale entry's life.
type StaleCache interface {
	Cache
	// GetStale is Get that also returns an entry kept past an invalidation or its
	// expiry, with stale true. found is false only when key has no entry at all.
	GetStale(ctx context.Context, key string) (content []byte, etag string, stale, found bool)
	// MarkStaleKey keeps key's entry but marks it stale, so Get no longer reports
	// it and GetStale reports it stale. Its content, ETag, expiry and tags are
	// unchanged. Marking a key that has no entry is not an error. A later write to
	// key stores a fresh entry, which clears the mark.
	MarkStaleKey(ctx context.Context, key string) error
	// MarkStale marks every entry associated with any of tags stale, as
	// MarkStaleKey does for one key.
	MarkStale(ctx context.Context, tags []string) error
}

// Entry is one stored cache record: rendered content, its ETag, expiry, the tags it
// was written with, and its insertion sequence number.
type Entry struct {
	// Content is the cached, rendered bytes.
	Content []byte
	// ETag is the strong ETag for Content, as returned by ETag(Content).
	ETag string
	// ExpiresAt is when the entry becomes stale. The zero time.Time means the
	// entry never expires.
	ExpiresAt time.Time
	// Tags are the dependency tags this entry was written with.
	Tags []string
	// Sequence is the entry's insertion order, assigned by the cache at write
	// time. It is used to determine the oldest entry for FIFO eviction; it is not
	// an LRU recency counter, so reading an entry never changes its Sequence.
	Sequence uint64
}
