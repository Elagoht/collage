package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// TieredCache puts a MemoryCache in front of a DiskCache. The disk tier is what
// survives a restart; the memory tier is what a hit costs, since a disk read is an
// open, a read and a JSON decode on every request. It implements Cache,
// TaggedCache and StaleCache.
//
// Writes go to the disk first and then to memory; reads ask memory first and copy a
// disk hit into memory as it is. A disk failure never stops the memory tier from
// being written, so a request is still served from memory and the caller is told.
type TieredCache struct {
	front *MemoryCache
	back  *DiskCache

	// mu and gen keep a promotion from bringing back an entry that a write or an
	// invalidation has just replaced. A promotion reads the disk without any lock,
	// so a mutation can run, on both tiers, between that read and the copy into
	// memory; the copy would then put the old entry back in memory, where it would
	// be served until it expired.
	//
	// Each mutation bumps gen before and after it touches the tiers, holding mu's
	// read lock throughout. A promotion notes gen before reading the disk, and
	// copies only under mu's write lock with gen unchanged. The write lock waits
	// for a mutation already in flight, whose second bump then shows; a mutation
	// that starts later waits for the copy, and so overwrites or removes it.
	// Mutations do not wait on each other, only on the rare promotion.
	mu  sync.RWMutex
	gen atomic.Uint64
}

var (
	_ Cache       = (*TieredCache)(nil)
	_ TaggedCache = (*TieredCache)(nil)
	_ StaleCache  = (*TieredCache)(nil)
)

// NewTiered puts front (memory) before back (disk). It warms front from back.
//
// Warm-up copies the newest entries on disk into memory, stale ones included,
// until the next one would make memory evict: a restarted process then answers
// its most recent pages from memory at once, and keeps the rest a promotion away.
// Entries go in oldest first, so memory's FIFO order matches the disk's age and the
// first entry memory evicts later is the oldest it holds. A file that cannot be
// read, or was written before headers carried their key, is skipped silently: it
// is still on disk and still served by a read.
func NewTiered(front *MemoryCache, back *DiskCache) *TieredCache {
	t := &TieredCache{front: front, back: back}
	t.warm()
	return t
}

// warmed is one disk entry chosen for warm-up.
type warmed struct {
	key   string
	entry Entry
	stale bool
}

func (t *TieredCache) warm() {
	entries, size := t.front.room()
	var chosen []warmed
	for _, path := range t.back.newestFirst() {
		if entries == 0 {
			break
		}
		h, content, err := readEntry(path)
		if err != nil || h.Key == "" {
			continue
		}
		if want, ok := t.back.path(h.Key); !ok || want != path {
			continue // a header that names some other file's key
		}
		if size >= 0 && int64(len(content)) > size {
			break
		}
		e, stale := t.back.entryOf(h, content)
		chosen = append(chosen, warmed{h.Key, e, stale})
		if entries > 0 {
			entries--
		}
		if size >= 0 {
			size -= int64(len(content))
		}
	}
	for i := len(chosen) - 1; i >= 0; i-- {
		t.front.put(chosen[i].key, chosen[i].entry, chosen[i].stale)
	}
}

// Dir is the disk tier's directory, version subdirectory included.
func (t *TieredCache) Dir() string { return t.back.Dir() }

// Get returns key's fresh entry from memory, or from disk when memory has none.
func (t *TieredCache) Get(ctx context.Context, key string) ([]byte, string, bool) {
	content, etag, stale, found := t.GetStale(ctx, key)
	if !found || stale {
		return nil, "", false
	}
	return content, etag, true
}

// GetStale returns key's entry, fresh or stale, from memory, or from disk when
// memory has none. Memory's answer is final once it has the key: it holds the same
// entry the disk does, so asking the disk again would only cost a read. A disk hit
// is copied into memory with its expiry, tags and stale mark unchanged.
func (t *TieredCache) GetStale(ctx context.Context, key string) ([]byte, string, bool, bool) {
	if ctx.Err() != nil {
		return nil, "", false, false
	}
	if content, etag, stale, found := t.front.GetStale(ctx, key); found {
		return content, etag, stale, true
	}

	gen := t.gen.Load()
	e, stale, found := t.back.load(key)
	if !found {
		return nil, "", false, false
	}
	t.mu.Lock()
	if t.gen.Load() == gen {
		t.front.put(key, e, stale)
	}
	t.mu.Unlock()
	return e.Content, e.ETag, stale, true
}

// Set stores content under key with no tags, as SetTagged.
func (t *TieredCache) Set(ctx context.Context, key string, content []byte, ttl time.Duration) (string, error) {
	return t.SetTagged(ctx, key, content, ttl, nil)
}

// SetTagged stores content in the disk tier and then the memory tier. A disk
// error is returned, but only after memory has been written, so the page is
// served from memory and the caller can report the failure.
//
// A ttl of zero or less becomes the disk tier's default before either tier sees
// it. Memory reads a zero default as never expiring while the disk does not, so
// leaving each to its own default would keep a page fresh in memory long after
// the disk called it expired.
func (t *TieredCache) SetTagged(ctx context.Context, key string, content []byte, ttl time.Duration, tags []string) (string, error) {
	if ttl <= 0 {
		ttl = t.back.defaultTTL
	}
	err := t.mutate(func() error {
		_, err := t.back.SetTagged(ctx, key, content, ttl, tags)
		return err
	}, func() error {
		_, err := t.front.SetTagged(ctx, key, content, ttl, tags)
		return err
	})
	return ETag(content), err
}

// mutate runs back and then front under the promotion guard described on
// TieredCache. front runs whatever back returned, so memory is always applied;
// their errors are joined.
func (t *TieredCache) mutate(back, front func() error) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	t.gen.Add(1)
	defer t.gen.Add(1)
	backErr := back()
	return errors.Join(backErr, front())
}

// InvalidateKey removes key from both tiers.
func (t *TieredCache) InvalidateKey(ctx context.Context, key string) error {
	err := t.mutate(
		func() error { return t.back.InvalidateKey(ctx, key) },
		func() error { return t.front.InvalidateKey(ctx, key) })
	return err
}

// Invalidate removes every entry carrying any of tags from both tiers.
func (t *TieredCache) Invalidate(ctx context.Context, tags []string) error {
	err := t.mutate(
		func() error { return t.back.Invalidate(ctx, tags) },
		func() error { return t.front.Invalidate(ctx, tags) })
	return err
}

// MarkStaleKey marks key stale in both tiers.
func (t *TieredCache) MarkStaleKey(ctx context.Context, key string) error {
	err := t.mutate(
		func() error { return t.back.MarkStaleKey(ctx, key) },
		func() error { return t.front.MarkStaleKey(ctx, key) })
	return err
}

// MarkStale marks every entry carrying any of tags stale in both tiers.
func (t *TieredCache) MarkStale(ctx context.Context, tags []string) error {
	err := t.mutate(
		func() error { return t.back.MarkStale(ctx, tags) },
		func() error { return t.front.MarkStale(ctx, tags) })
	return err
}

// Clear empties both tiers.
func (t *TieredCache) Clear(ctx context.Context) error {
	err := t.mutate(
		func() error { return t.back.Clear(ctx) },
		func() error { return t.front.Clear(ctx) })
	return err
}
