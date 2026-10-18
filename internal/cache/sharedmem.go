package cache

import (
	"context"
	"slices"
	"sync"
	"time"
)

// sharedEntry is one stored page or data value. Pages and data share the shape;
// a data entry has no meta and no ETag.
type sharedEntry struct {
	content   []byte
	etag      string
	meta      []byte
	expiresAt time.Time
	tags      []string
	// stale is set by an invalidation and cleared by the next write to the key,
	// which builds a new entry.
	stale bool
	// since is the epoch the write that made the entry began at; 0 for an
	// unconditional write, which no overlapped write then defers to.
	since uint64
}

// SharedMemory is the reference in-memory SharedCache. It holds everything under
// one mutex, so it is safe for concurrent use, and two Apps given the same
// *SharedMemory behave as two instances sharing one Redis: that is what tests of
// the shared-store paths run against, and what a store implementation's own
// conformance run is compared with.
//
// Page writes with a ttl of zero or less never expire: unlike a MemoryCache it
// has no configured default TTL, and it has no size cap, so an entry lives until
// it is invalidated, purged, overwritten or the store is cleared. Expiry uses
// real time. Expired entries, like invalidated ones, are kept and reported stale.
type SharedMemory struct {
	mu    sync.Mutex
	epoch uint64
	pages map[string]*sharedEntry
	data  map[string]*sharedEntry
	// pageTags and dataTags index tag to the keys written under it. They are kept
	// apart because a key may be both a page and a data key.
	pageTags map[string]map[string]struct{}
	dataTags map[string]map[string]struct{}
	// invalidated and purged are each tag's generation: the epoch of the last
	// DropTags that invalidated or purged it.
	invalidated map[string]uint64
	purged      map[string]uint64
}

var _ SharedCache = (*SharedMemory)(nil)

// NewSharedMemory returns an empty SharedMemory. Its epoch starts at 1, so a
// since of 0 is older than every drop.
func NewSharedMemory() *SharedMemory {
	return &SharedMemory{
		epoch:       1,
		pages:       make(map[string]*sharedEntry),
		data:        make(map[string]*sharedEntry),
		pageTags:    make(map[string]map[string]struct{}),
		dataTags:    make(map[string]map[string]struct{}),
		invalidated: make(map[string]uint64),
		purged:      make(map[string]uint64),
	}
}

// Pages returns how many page entries are stored, stale ones included.
func (c *SharedMemory) Pages() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pages)
}

// Data returns how many data values are stored, stale ones included.
func (c *SharedMemory) Data() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.data)
}

func (c *SharedMemory) expired(e *sharedEntry) bool {
	return !e.expiresAt.IsZero() && !time.Now().Before(e.expiresAt)
}

// Epoch returns the current epoch.
func (c *SharedMemory) Epoch(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch, nil
}

// Get returns the fresh page entry for key.
func (c *SharedMemory) Get(ctx context.Context, key string) ([]byte, string, bool) {
	content, etag, stale, found := c.GetStale(ctx, key)
	if !found || stale {
		return nil, "", false
	}
	return content, etag, true
}

// GetStale returns key's page entry, fresh or not.
func (c *SharedMemory) GetStale(ctx context.Context, key string) ([]byte, string, bool, bool) {
	if ctx.Err() != nil {
		return nil, "", false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.pages[key]
	if !ok {
		return nil, "", false, false
	}
	return slices.Clone(e.content), e.etag, e.stale || c.expired(e), true
}

// Set stores a page entry with no tags.
func (c *SharedMemory) Set(ctx context.Context, key string, content []byte, ttl time.Duration) (string, error) {
	return c.SetTagged(ctx, key, content, ttl, nil)
}

// SetTagged stores a page entry with tags, unconditionally.
func (c *SharedMemory) SetTagged(ctx context.Context, key string, content []byte, ttl time.Duration, tags []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.putPageLocked(key, content, ttl, tags, nil, false, 0), nil
}

// SetSince stores a page entry unless, or stale if, a tag was dropped after since.
func (c *SharedMemory) SetSince(ctx context.Context, key string, content []byte, ttl time.Duration, tags []string, meta []byte, since uint64) (string, WriteOutcome, error) {
	if err := ctx.Err(); err != nil {
		return "", WriteSkipped, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	outcome, generation := c.outcomeLocked(tags, since)
	switch {
	case outcome == WriteSkipped:
		return "", WriteSkipped, nil
	case outcome == WriteStale && c.newerFresh(c.pages[key], generation):
		return "", WriteStale, nil
	}
	return c.putPageLocked(key, content, ttl, tags, meta, outcome == WriteStale, since), outcome, nil
}

// outcomeLocked is the rule SetSince and SetData share: a purge after since
// skips the write, else an invalidation after since stores it stale.
// generation is the highest invalidation generation among tags, which a stale
// outcome compares the entry already stored with (see newerFresh).
func (c *SharedMemory) outcomeLocked(tags []string, since uint64) (outcome WriteOutcome, generation uint64) {
	for _, t := range tags {
		if c.purged[t] > since {
			return WriteSkipped, 0
		}
	}
	for _, t := range tags {
		generation = max(generation, c.invalidated[t])
	}
	if generation > since {
		return WriteStale, generation
	}
	return WriteFresh, generation
}

// newerFresh reports whether e, the entry a stale write would replace, is
// fresh and was written by a render that began at or after generation — after
// the drop that makes the new write stale. It holds newer content than the
// overlapped write, which began before that drop, and is kept: replaced, a
// fleet would serve the older render, stale, where it had a fresh copy of the
// newer one.
func (c *SharedMemory) newerFresh(e *sharedEntry, generation uint64) bool {
	return e != nil && !e.stale && !c.expired(e) && e.since >= generation
}

func (c *SharedMemory) putPageLocked(key string, content []byte, ttl time.Duration, tags []string, meta []byte, stale bool, since uint64) string {
	etag := ETag(content)
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	}
	c.removePageLocked(key)
	c.pages[key] = &sharedEntry{
		content:   slices.Clone(content),
		etag:      etag,
		meta:      slices.Clone(meta),
		expiresAt: expiresAt,
		tags:      slices.Clone(tags),
		stale:     stale,
		since:     since,
	}
	indexTags(c.pageTags, key, tags)
	return etag
}

func indexTags(index map[string]map[string]struct{}, key string, tags []string) {
	for _, t := range tags {
		set, ok := index[t]
		if !ok {
			set = make(map[string]struct{})
			index[t] = set
		}
		set[key] = struct{}{}
	}
}

func unindexTags(index map[string]map[string]struct{}, key string, tags []string) {
	for _, t := range tags {
		if set, ok := index[t]; ok {
			delete(set, key)
			if len(set) == 0 {
				delete(index, t)
			}
		}
	}
}

func (c *SharedMemory) removePageLocked(key string) {
	if e, ok := c.pages[key]; ok {
		unindexTags(c.pageTags, key, e.tags)
		delete(c.pages, key)
	}
}

func (c *SharedMemory) removeDataLocked(key string) {
	if e, ok := c.data[key]; ok {
		unindexTags(c.dataTags, key, e.tags)
		delete(c.data, key)
	}
}

// MarkStaleKey marks key's page entry stale, keeping everything else.
func (c *SharedMemory) MarkStaleKey(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.pages[key]; ok {
		e.stale = true
	}
	return nil
}

// MarkStale marks every page entry under any of tags stale. It does not touch
// the tags' generations: only DropTags orders a drop against in-flight writes.
func (c *SharedMemory) MarkStale(ctx context.Context, tags []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range tags {
		for key := range c.pageTags[t] {
			c.pages[key].stale = true
		}
	}
	return nil
}

// InvalidateKey removes key's page entry.
func (c *SharedMemory) InvalidateKey(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removePageLocked(key)
	return nil
}

// Invalidate removes every page entry under any of tags.
func (c *SharedMemory) Invalidate(ctx context.Context, tags []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range tags {
		for _, key := range sortedKeys(c.pageTags[t]) {
			c.removePageLocked(key)
		}
	}
	return nil
}

// Clear removes every page entry and data value. Tag generations and the epoch
// are kept, since the epoch must never go back.
func (c *SharedMemory) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pages = make(map[string]*sharedEntry)
	c.data = make(map[string]*sharedEntry)
	c.pageTags = make(map[string]map[string]struct{})
	c.dataTags = make(map[string]map[string]struct{})
	return nil
}

// sortedKeys copies a key set into a slice, sorted so DropTags answers in a
// stable order and so removing while ranging is safe.
func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// DropTags is one atomic step under the mutex: the epoch advances once, each tag
// takes the new epoch as its generation, and every entry under a tag is marked
// stale (or removed, for a purge). Only page entries are returned, each once,
// sorted by key.
func (c *SharedMemory) DropTags(ctx context.Context, tags []string, purge bool) ([]Reached, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++

	reachedKeys := make(map[string]struct{})
	dataKeys := make(map[string]struct{})
	for _, t := range tags {
		if purge {
			c.purged[t] = c.epoch
		} else {
			c.invalidated[t] = c.epoch
		}
		for k := range c.pageTags[t] {
			reachedKeys[k] = struct{}{}
		}
		for k := range c.dataTags[t] {
			dataKeys[k] = struct{}{}
		}
	}

	reached := make([]Reached, 0, len(reachedKeys))
	for _, k := range sortedKeys(reachedKeys) {
		e := c.pages[k]
		reached = append(reached, Reached{Key: k, Meta: slices.Clone(e.meta)})
		if purge {
			c.removePageLocked(k)
		} else {
			e.stale = true
		}
	}
	for k := range dataKeys {
		if purge {
			c.removeDataLocked(k)
		} else {
			c.data[k].stale = true
		}
	}
	return reached, nil
}

// GetData returns key's data value; an expired or invalidated one is found and
// stale.
func (c *SharedMemory) GetData(ctx context.Context, key string) ([]byte, bool, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.data[key]
	if !ok {
		return nil, false, false, nil
	}
	return slices.Clone(e.content), e.stale || c.expired(e), true, nil
}

// SetData stores a data value, subject to since as SetSince is. A ttl of zero or
// less never expires.
func (c *SharedMemory) SetData(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string, since uint64) (WriteOutcome, error) {
	if err := ctx.Err(); err != nil {
		return WriteSkipped, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	outcome, generation := c.outcomeLocked(tags, since)
	switch {
	case outcome == WriteSkipped:
		return WriteSkipped, nil
	case outcome == WriteStale && c.newerFresh(c.data[key], generation):
		return WriteStale, nil
	}
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	}
	c.removeDataLocked(key)
	c.data[key] = &sharedEntry{
		content:   slices.Clone(value),
		expiresAt: expiresAt,
		tags:      slices.Clone(tags),
		stale:     outcome == WriteStale,
		since:     since,
	}
	indexTags(c.dataTags, key, tags)
	return outcome, nil
}

// DeleteData removes key's data value; a missing key is not an error.
func (c *SharedMemory) DeleteData(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeDataLocked(key)
	return nil
}
