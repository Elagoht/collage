package cache

import (
	"context"
	"time"
)

// WriteOutcome is what a conditional write did.
type WriteOutcome int

const (
	// WriteFresh means the entry was stored as a fresh entry.
	WriteFresh WriteOutcome = iota
	// WriteStale means the entry was stored but marked stale: a tag it carries was
	// invalidated after the write's since epoch.
	WriteStale
	// WriteSkipped means the entry was not stored: a tag it carries was purged
	// after the write's since epoch.
	WriteSkipped
)

// Reached is one page entry a DropTags reached, with the meta it was written
// with. The caller uses the meta to re-render or re-announce the page without
// holding a per-instance index of what the store contains.
type Reached struct {
	// Key is the page entry's key.
	Key string
	// Meta is the opaque bytes the entry was written with through SetSince.
	Meta []byte
}

// SharedCache is implemented by a store that a fleet of instances share, such as
// one backed by Redis. Because every instance reads and writes the same entries,
// invalidation cannot rely on per-process state: it is ordered by an epoch the
// store owns, and a write that raced an invalidation is detected by comparing the
// epoch it started at with the generation of each tag it carries. Keys and tags
// are opaque; the store owns its own namespacing.
//
// A SharedCache is also a TaggedCache and a StaleCache, so every page operation
// works as it does for a single-process store.
type SharedCache interface {
	TaggedCache
	StaleCache
	// Epoch returns the store's current epoch. It only grows, including across a
	// restart or flush of the backing store, so a since value read earlier never
	// compares newer than one read later.
	Epoch(ctx context.Context) (uint64, error)
	// SetSince stores a page entry like SetTagged, together with meta, but only as
	// far as no tag was dropped after since: WriteSkipped if any tag's purge
	// generation is greater than since; else WriteStale if any tag's invalidation
	// generation is greater than since; else WriteFresh. A ttl of zero or less is
	// the default TTL, as Set. A write whose outcome is WriteStale leaves in place,
	// unwritten, an entry under key that is fresh and was written by a SetSince
	// whose since is at least the highest invalidation generation among the new
	// write's tags — a newer render's — and still returns WriteStale; an entry
	// from an unconditional write counts as written at since 0.
	SetSince(ctx context.Context, key string, content []byte, ttl time.Duration, tags []string, meta []byte, since uint64) (etag string, outcome WriteOutcome, err error)
	// DropTags advances the epoch once, sets each tag's invalidation generation
	// (purge false) or purge generation (purge true) to the new epoch, marks stale
	// (or removes) every page and data entry under any tag, and returns the page
	// entries reached (data entries are not returned) — as one atomic step.
	DropTags(ctx context.Context, tags []string, purge bool) ([]Reached, error)
	// GetData returns the data value stored under key. An expired value is found
	// and stale; found is false only when key has no value at all.
	GetData(ctx context.Context, key string) (value []byte, stale, found bool, err error)
	// SetData stores a data value under key with tags, subject to since exactly as
	// SetSince is, including leaving a newer fresh value in place. A ttl of zero
	// means the value never expires.
	SetData(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string, since uint64) (WriteOutcome, error)
	// DeleteData removes the data value stored under key. Deleting a key that has
	// no value is not an error.
	DeleteData(ctx context.Context, key string) error
}
