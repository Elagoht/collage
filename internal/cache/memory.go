package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// defaultMaxEntries is the entry cap MemoryConfig.MaxEntries uses when left at its
// zero value, matching the project-wide convention: zero means "use the default",
// a negative value means unlimited.
const defaultMaxEntries = 10000

// DefaultMemoryMaxBytes is the content weight MemoryConfig.MaxBytes caps a memory
// cache at when left at zero: 256 MiB.
const DefaultMemoryMaxBytes int64 = 256 << 20

// MemoryConfig configures a MemoryCache.
type MemoryConfig struct {
	// DefaultTTL is the entry lifetime Set and SetTagged use when called with
	// ttl <= 0. A DefaultTTL of zero means such entries never expire.
	DefaultTTL time.Duration
	// MaxEntries caps the number of stored entries. Zero means "use the default"
	// (10000); a negative value means unlimited. Eviction at the cap is FIFO —
	// the oldest entry by insertion sequence is dropped, not the least recently
	// used one — so callers must not assume LRU semantics.
	MaxEntries int
	// MaxBytes caps the total size of the stored content, in bytes. Zero means
	// "use the default" (DefaultMemoryMaxBytes); a negative value means unlimited.
	// Past it the oldest entries are evicted, as for MaxEntries, and an entry
	// larger than the whole cap is not stored at all. MaxEntries counts entries,
	// not what they weigh: a client asking for one large page under thousands of
	// invented queries fills a cache with copies of it.
	MaxBytes int64
	// Now returns the current time, used for expiry checks. Defaults to
	// time.Now. Tests should inject a controllable clock here instead of
	// sleeping to exercise expiry.
	Now func() time.Time
}

// node is one entry in the FIFO insertion-order queue used for eviction. It is a
// hand-rolled doubly linked list (rather than container/list) so that removal
// never needs a type assertion out of an untyped Value.
type node struct {
	key  string
	prev *node
	next *node
}

// record is a stored entry plus its position in the FIFO insertion-order queue.
type record struct {
	entry Entry
	pos   *node
	// stale is set by MarkStaleKey and MarkStale. It lives here rather than on
	// Entry because only the cache reads it: a write builds a new record, which is
	// what clears it.
	stale bool
}

// MemoryCache is an in-memory, process-local implementation of Cache,
// TaggedCache and StaleCache. It evicts FIFO — oldest by insertion sequence, not
// least recently used — once MemoryConfig.MaxEntries or MaxBytes is exceeded. An
// expired entry is not deleted but kept as stale, so eviction is the only thing
// that removes an entry nobody invalidates.
type MemoryCache struct {
	mu      sync.RWMutex
	cfg     MemoryConfig
	entries map[string]*record
	tags    map[string]map[string]struct{}
	head    *node // oldest inserted, evicted first
	tail    *node // most recently inserted
	seq     uint64
	// bytes is the total length of every stored entry's content.
	bytes int64

	hits      atomic.Uint64
	misses    atomic.Uint64
	sets      atomic.Uint64
	evictions atomic.Uint64
}

var (
	_ Cache       = (*MemoryCache)(nil)
	_ TaggedCache = (*MemoryCache)(nil)
	_ StaleCache  = (*MemoryCache)(nil)
)

// Stats reports MemoryCache's cumulative counters, for observability.
type Stats struct {
	// Hits counts Get and GetStale calls that found a fresh entry.
	Hits uint64
	// Misses counts Get and GetStale calls that found no fresh entry, including
	// an entry that had expired or been marked stale. GetStale counts too because
	// a handler reading through it would otherwise leave both counters at zero.
	Misses uint64
	// Sets counts successful Set and SetTagged calls.
	Sets uint64
	// Evictions counts entries removed by FIFO eviction at MaxEntries.
	Evictions uint64
	// Entries is the number of entries currently stored.
	Entries uint64
	// Bytes is the total size of the content currently stored.
	Bytes int64
}

// NewMemory creates a MemoryCache configured by cfg. A zero cfg.MaxEntries becomes
// the project-wide default (10000); a negative value means unlimited and disables
// eviction. A nil cfg.Now defaults to time.Now.
func NewMemory(cfg MemoryConfig) *MemoryCache {
	if cfg.MaxEntries == 0 {
		cfg.MaxEntries = defaultMaxEntries
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = DefaultMemoryMaxBytes
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &MemoryCache{
		cfg:     cfg,
		entries: make(map[string]*record),
		tags:    make(map[string]map[string]struct{}),
	}
}

// Get returns the cached content and ETag for key. found is false when there is no
// fresh entry, including one that has expired or been marked stale. Neither is
// deleted: it stays for GetStale until it is rewritten, invalidated or evicted.
func (c *MemoryCache) Get(ctx context.Context, key string) ([]byte, string, bool) {
	content, etag, stale, found := c.GetStale(ctx, key)
	if !found || stale {
		return nil, "", false
	}
	return content, etag, true
}

// GetStale returns key's entry whether it is fresh or not; stale is true when it
// has expired or been marked stale. It only takes the read lock, since nothing
// is deleted on the way out.
func (c *MemoryCache) GetStale(ctx context.Context, key string) ([]byte, string, bool, bool) {
	if ctx.Err() != nil {
		return nil, "", false, false
	}

	c.mu.RLock()
	rec, ok := c.entries[key]
	if !ok {
		c.mu.RUnlock()
		c.misses.Add(1)
		return nil, "", false, false
	}
	content, etag := rec.entry.Content, rec.entry.ETag
	stale := rec.stale || c.isExpired(rec.entry.ExpiresAt)
	c.mu.RUnlock()

	if stale {
		c.misses.Add(1)
	} else {
		c.hits.Add(1)
	}
	return content, etag, stale, true
}

// MarkStaleKey marks key's entry stale, keeping its content, expiry, tags and
// FIFO position: being marked is not a new insertion, so it does not buy the
// entry a longer life under eviction. A key with no entry is not an error.
func (c *MemoryCache) MarkStaleKey(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if rec, ok := c.entries[key]; ok {
		rec.stale = true
	}
	return nil
}

// MarkStale marks every entry associated with any of tags stale, as MarkStaleKey
// does. The tag index is left as it is, so a later Invalidate still reaches them.
func (c *MemoryCache) MarkStale(ctx context.Context, tags []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, tag := range tags {
		for key := range c.tags[tag] {
			if rec, ok := c.entries[key]; ok {
				rec.stale = true
			}
		}
	}
	return nil
}

// Set stores content under key with no tags. It is equivalent to calling
// SetTagged with a nil tags slice.
func (c *MemoryCache) Set(ctx context.Context, key string, content []byte, ttl time.Duration) (string, error) {
	return c.SetTagged(ctx, key, content, ttl, nil)
}

// SetTagged stores content under key with the given ttl and tags, returning the
// content's ETag. A ttl of zero or less uses cfg.DefaultTTL; a DefaultTTL of zero
// means the entry never expires. Setting a key that already has an entry replaces
// it entirely — content, ETag, expiry, tags and any stale mark — and refreshes its
// FIFO position, since the replacement is itself a new insertion. If the cache is now over
// MaxEntries, the oldest entry by insertion sequence is evicted.
func (c *MemoryCache) SetTagged(ctx context.Context, key string, content []byte, ttl time.Duration, tags []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	etag := ETag(content)
	if ttl <= 0 {
		ttl = c.cfg.DefaultTTL
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = c.cfg.Now().Add(ttl)
	}

	if _, exists := c.entries[key]; exists {
		c.removeLocked(key)
	}
	if c.cfg.MaxBytes > 0 && int64(len(content)) > c.cfg.MaxBytes {
		// Larger than the whole cache: storing it would evict everything else
		// and then itself. Served, not kept; the entry it replaces is gone,
		// because that one is stale now.
		return etag, nil
	}

	c.seq++
	pos := c.pushBackLocked(key)
	c.entries[key] = &record{
		entry: Entry{
			Content:   append([]byte(nil), content...),
			ETag:      etag,
			ExpiresAt: expiresAt,
			Tags:      append([]string(nil), tags...),
			Sequence:  c.seq,
		},
		pos: pos,
	}
	c.bytes += int64(len(content))

	for _, tag := range tags {
		set, ok := c.tags[tag]
		if !ok {
			set = make(map[string]struct{})
			c.tags[tag] = set
		}
		set[key] = struct{}{}
	}

	c.sets.Add(1)
	c.evictIfNeededLocked()

	return etag, nil
}

// Invalidate removes every entry associated with any of tags, and drops each of
// those tags from the tag index since none of its keys remain.
func (c *MemoryCache) Invalidate(ctx context.Context, tags []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, tag := range tags {
		keys, ok := c.tags[tag]
		if !ok {
			continue
		}
		// Copy the key set before removing: removeLocked mutates c.tags[tag] (and
		// deletes the tag entirely once its key set empties), so ranging over the
		// live map while removeLocked drains it would be unsafe.
		toRemove := make([]string, 0, len(keys))
		for k := range keys {
			toRemove = append(toRemove, k)
		}
		for _, k := range toRemove {
			c.removeLocked(k)
		}
	}
	return nil
}

// InvalidateKey removes the entry stored under key, if any, along with every tag
// reference to it. Invalidating a key with no entry is not an error.
func (c *MemoryCache) InvalidateKey(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(key)
	return nil
}

// Clear removes every entry and the entire tag index.
func (c *MemoryCache) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*record)
	c.tags = make(map[string]map[string]struct{})
	c.head = nil
	c.tail = nil
	c.bytes = 0
	return nil
}

// Stats returns a snapshot of the cache's cumulative counters.
func (c *MemoryCache) Stats() Stats {
	c.mu.RLock()
	entries := uint64(len(c.entries))
	size := c.bytes
	c.mu.RUnlock()

	return Stats{
		Hits:      c.hits.Load(),
		Misses:    c.misses.Load(),
		Sets:      c.sets.Load(),
		Evictions: c.evictions.Load(),
		Entries:   entries,
		Bytes:     size,
	}
}

// isExpired reports whether expiresAt has passed according to cfg.Now. The zero
// time.Time means "never expires".
func (c *MemoryCache) isExpired(expiresAt time.Time) bool {
	if expiresAt.IsZero() {
		return false
	}
	return !c.cfg.Now().Before(expiresAt)
}

// removeLocked deletes key's entry from c.entries and the FIFO queue, and removes
// key from every tag set in c.tags that referenced it, dropping any tag whose key
// set becomes empty as a result. It is a no-op if key has no entry. Callers must
// hold c.mu for writing.
func (c *MemoryCache) removeLocked(key string) {
	rec, ok := c.entries[key]
	if !ok {
		return
	}
	delete(c.entries, key)
	c.unlinkLocked(rec.pos)
	c.bytes -= int64(len(rec.entry.Content))

	for _, tag := range rec.entry.Tags {
		keys, ok := c.tags[tag]
		if !ok {
			continue
		}
		delete(keys, key)
		if len(keys) == 0 {
			delete(c.tags, tag)
		}
	}
}

// evictIfNeededLocked evicts the oldest entry, by insertion sequence, while the
// cache holds more than cfg.MaxEntries entries or more than cfg.MaxBytes of
// content. A negative value disables that cap. Callers must hold c.mu for writing.
func (c *MemoryCache) evictIfNeededLocked() {
	over := func() bool {
		return (c.cfg.MaxEntries >= 0 && len(c.entries) > c.cfg.MaxEntries) ||
			(c.cfg.MaxBytes > 0 && c.bytes > c.cfg.MaxBytes)
	}
	for over() && c.head != nil {
		key := c.head.key
		c.removeLocked(key)
		c.evictions.Add(1)
	}
}

// pushBackLocked appends key to the tail of the FIFO queue and returns its node.
// Callers must hold c.mu for writing.
func (c *MemoryCache) pushBackLocked(key string) *node {
	n := &node{key: key}
	if c.tail == nil {
		c.head = n
		c.tail = n
		return n
	}
	n.prev = c.tail
	c.tail.next = n
	c.tail = n
	return n
}

// unlinkLocked removes n from the FIFO queue, patching up c.head/c.tail and its
// neighbours as needed. Callers must hold c.mu for writing.
func (c *MemoryCache) unlinkLocked(n *node) {
	if n.prev != nil {
		n.prev.next = n.next
	} else {
		c.head = n.next
	}
	if n.next != nil {
		n.next.prev = n.prev
	} else {
		c.tail = n.prev
	}
	n.prev = nil
	n.next = nil
}
