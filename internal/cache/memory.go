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
}

// MemoryCache is an in-memory, process-local implementation of Cache and
// TaggedCache. It evicts FIFO — oldest by insertion sequence, not least recently
// used — once MemoryConfig.MaxEntries is exceeded, and expires entries lazily on
// Get.
type MemoryCache struct {
	mu      sync.RWMutex
	cfg     MemoryConfig
	entries map[string]*record
	tags    map[string]map[string]struct{}
	head    *node // oldest inserted, evicted first
	tail    *node // most recently inserted
	seq     uint64

	hits      atomic.Uint64
	misses    atomic.Uint64
	sets      atomic.Uint64
	evictions atomic.Uint64
}

var (
	_ Cache       = (*MemoryCache)(nil)
	_ TaggedCache = (*MemoryCache)(nil)
)

// Stats reports MemoryCache's cumulative counters, for observability.
type Stats struct {
	// Hits counts Get calls that returned a live entry.
	Hits uint64
	// Misses counts Get calls that found no live entry, including entries that
	// had expired.
	Misses uint64
	// Sets counts successful Set and SetTagged calls.
	Sets uint64
	// Evictions counts entries removed by FIFO eviction at MaxEntries.
	Evictions uint64
	// Entries is the number of entries currently stored.
	Entries uint64
}

// NewMemory creates a MemoryCache configured by cfg. A zero cfg.MaxEntries becomes
// the project-wide default (10000); a negative value means unlimited and disables
// eviction. A nil cfg.Now defaults to time.Now.
func NewMemory(cfg MemoryConfig) *MemoryCache {
	if cfg.MaxEntries == 0 {
		cfg.MaxEntries = defaultMaxEntries
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
// live entry, including one that has expired — an expired entry is lazily deleted
// as part of this call. Get only takes the write lock for that lazy delete; a plain
// hit or a miss on a key that was never present only takes the read lock.
func (c *MemoryCache) Get(ctx context.Context, key string) ([]byte, string, bool) {
	if ctx.Err() != nil {
		return nil, "", false
	}

	c.mu.RLock()
	rec, ok := c.entries[key]
	if ok && !c.isExpired(rec.entry.ExpiresAt) {
		content, etag := rec.entry.Content, rec.entry.ETag
		c.mu.RUnlock()
		c.hits.Add(1)
		return content, etag, true
	}
	c.mu.RUnlock()

	if !ok {
		c.misses.Add(1)
		return nil, "", false
	}

	// rec existed but looked expired under the read lock. Upgrade to the write
	// lock and re-check by key rather than trusting rec: another goroutine may
	// have deleted, replaced, or refreshed this key between the RUnlock above and
	// the Lock below.
	c.mu.Lock()
	rec, ok = c.entries[key]
	switch {
	case !ok:
		c.mu.Unlock()
		c.misses.Add(1)
		return nil, "", false
	case c.isExpired(rec.entry.ExpiresAt):
		c.removeLocked(key)
		c.mu.Unlock()
		c.misses.Add(1)
		return nil, "", false
	default:
		content, etag := rec.entry.Content, rec.entry.ETag
		c.mu.Unlock()
		c.hits.Add(1)
		return content, etag, true
	}
}

// Set stores content under key with no tags. It is equivalent to calling
// SetTagged with a nil tags slice.
func (c *MemoryCache) Set(ctx context.Context, key string, content []byte, ttl time.Duration) (string, error) {
	return c.SetTagged(ctx, key, content, ttl, nil)
}

// SetTagged stores content under key with the given ttl and tags, returning the
// content's ETag. A ttl of zero or less uses cfg.DefaultTTL; a DefaultTTL of zero
// means the entry never expires. Setting a key that already has an entry replaces
// it entirely — content, ETag, expiry, and tags — and refreshes its FIFO position,
// since the replacement is itself a new insertion. If the cache is now over
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
	return nil
}

// Stats returns a snapshot of the cache's cumulative counters.
func (c *MemoryCache) Stats() Stats {
	c.mu.RLock()
	entries := uint64(len(c.entries))
	c.mu.RUnlock()

	return Stats{
		Hits:      c.hits.Load(),
		Misses:    c.misses.Load(),
		Sets:      c.sets.Load(),
		Evictions: c.evictions.Load(),
		Entries:   entries,
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
// cache holds more than cfg.MaxEntries entries. A negative MaxEntries means
// unlimited and disables eviction entirely. Callers must hold c.mu for writing.
func (c *MemoryCache) evictIfNeededLocked() {
	if c.cfg.MaxEntries < 0 {
		return
	}
	for len(c.entries) > c.cfg.MaxEntries && c.head != nil {
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
