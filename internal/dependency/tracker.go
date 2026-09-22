// Package dependency tracks which cache keys were produced from which dependency
// tags, and resolves tags back to the keys they invalidate. Fragments emit
// dependency tags while rendering (for example "post:123" or "category:tech"); a
// rendered page's cache entry is derived from some set of those tags. When content
// changes, the application invalidates a tag, and this package works out which
// cached keys were built from it. The internal/cache package's Cache.Set
// deliberately carries no tags, so this package is the authority for resolving tags
// back to keys.
package dependency

import (
	"context"
	"sort"
	"sync"
)

// Tracker records which cache keys were produced from which dependency tags and
// resolves tags back to the keys they invalidate. An implementation's only defined
// failure mode is context cancellation, reported via the standard context.Canceled
// and context.DeadlineExceeded errors — this package defines no tracker-specific
// error sentinels.
type Tracker interface {
	// Track records that key was produced from tags, replacing any tag set
	// previously recorded for key. A key re-rendered with a narrower or different
	// tag set is removed from every tag it no longer belongs to. An empty tags
	// leaves key tracked with no tags, which is not an error.
	Track(ctx context.Context, key string, tags []string) error
	// Resolve returns the de-duplicated, sorted union of every key currently
	// tracked under any of tags. An empty tags, or a tag with no tracked keys,
	// contributes nothing and is not an error.
	Resolve(ctx context.Context, tags []string) ([]string, error)
	// Forget removes key and every association between it and its tracked tags.
	// Forgetting a key that is not tracked is not an error.
	Forget(ctx context.Context, key string) error
	// ForgetTags removes every one of tags, and the association between each of
	// them and the keys tracked under it. Keys that remain tracked under other
	// tags are otherwise left alone. An empty tags, or a tag with no tracked
	// keys, is not an error.
	ForgetTags(ctx context.Context, tags []string) error
	// Tags returns the de-duplicated, sorted set of tags currently recorded for
	// key. A key that is not tracked returns an empty slice, not an error.
	Tags(ctx context.Context, key string) ([]string, error)
	// Clear removes every tracked key and tag.
	Clear(ctx context.Context) error
}

// keyNode is one entry in a tag's FIFO insertion-order list of keys. It is a
// hand-rolled doubly linked list — mirroring the internal/cache package's pattern —
// so that dropping the oldest key for a tag never needs a type assertion out of an
// untyped Value, the way container/list's Value field would require.
type keyNode struct {
	key  string
	prev *keyNode
	next *keyNode
}

// tagEntry holds the keys currently tracked under one tag, both for O(1) membership
// lookup and, via the FIFO list, in insertion order so MaxKeysPerTag eviction can
// find the oldest key without scanning.
type tagEntry struct {
	nodes map[string]*keyNode
	head  *keyNode // oldest inserted, dropped first
	tail  *keyNode // most recently inserted
}

// pushBack appends key to the tail of e's FIFO list and indexes it in e.nodes.
func (e *tagEntry) pushBack(key string) {
	n := &keyNode{key: key}
	if e.tail == nil {
		e.head = n
		e.tail = n
	} else {
		n.prev = e.tail
		e.tail.next = n
		e.tail = n
	}
	e.nodes[key] = n
}

// unlink removes n from e's FIFO list, patching up e.head/e.tail and its neighbours,
// and drops it from e.nodes. It does not touch any structure outside e.
func (e *tagEntry) unlink(n *keyNode) {
	if n.prev != nil {
		n.prev.next = n.next
	} else {
		e.head = n.next
	}
	if n.next != nil {
		n.next.prev = n.prev
	} else {
		e.tail = n.prev
	}
	n.prev = nil
	n.next = nil
	delete(e.nodes, n.key)
}

// MemoryTracker is an in-memory, process-local implementation of Tracker. It keeps
// both directions of the tag/key relationship — tag to keys and key to tags — under
// one sync.RWMutex, so every mutation updates both sides consistently and Forget can
// remove a key from every tag it belongs to without scanning the whole index.
type MemoryTracker struct {
	// MaxKeysPerTag caps the number of keys tracked under a single tag. Zero (the
	// zero value) means unlimited. When a tag would exceed the cap, the oldest
	// key tracked under it — by insertion order into that tag, not overall — is
	// dropped and Stats().Dropped is incremented. Set it before the tracker is
	// used concurrently; MemoryTracker does not synchronize reads of this field
	// against writes to it.
	MaxKeysPerTag int

	mu        sync.RWMutex
	keyToTags map[string]map[string]struct{}
	tagToKeys map[string]*tagEntry
	dropped   uint64
}

var _ Tracker = (*MemoryTracker)(nil)

// Stats reports MemoryTracker's current size and cumulative eviction count.
type Stats struct {
	// Keys is the number of keys currently tracked under at least one tag.
	Keys uint64
	// Tags is the number of tags currently tracking at least one key.
	Tags uint64
	// Dropped is the cumulative count of keys removed from a tag by
	// MaxKeysPerTag eviction. It does not reset on Clear.
	Dropped uint64
}

// NewMemory creates an empty MemoryTracker. MaxKeysPerTag defaults to zero
// (unlimited); set it on the returned tracker before concurrent use to bound growth.
func NewMemory() *MemoryTracker {
	return &MemoryTracker{
		keyToTags: make(map[string]map[string]struct{}),
		tagToKeys: make(map[string]*tagEntry),
	}
}

// Track records that key was produced from tags, replacing any tag set previously
// recorded for key: tags key no longer belongs to are dropped, tags it newly belongs
// to are added, and tags it already belonged to are left untouched (including their
// position in that tag's eviction order). Passing an empty tags leaves key tracked
// with no tags. Adding key to a tag that is over MaxKeysPerTag drops that tag's
// oldest key, which may or may not be key itself.
func (t *MemoryTracker) Track(ctx context.Context, key string, tags []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	newSet := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		newSet[tag] = struct{}{}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	oldSet := t.keyToTags[key]

	for tag := range oldSet {
		if _, stillTagged := newSet[tag]; !stillTagged {
			t.untrackKeyFromTagLocked(tag, key)
		}
	}

	for tag := range newSet {
		if _, alreadyTagged := oldSet[tag]; alreadyTagged {
			continue
		}
		t.trackKeyUnderTagLocked(tag, key)
	}

	if len(newSet) == 0 {
		delete(t.keyToTags, key)
	} else {
		t.keyToTags[key] = newSet
	}

	return nil
}

// Resolve returns the de-duplicated, sorted union of every key currently tracked
// under any of tags. An unknown tag, or an empty tags, contributes nothing.
func (t *MemoryTracker) Resolve(ctx context.Context, tags []string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	seen := make(map[string]struct{})
	for _, tag := range tags {
		entry, ok := t.tagToKeys[tag]
		if !ok {
			continue
		}
		for key := range entry.nodes {
			seen[key] = struct{}{}
		}
	}

	return sortedKeys(seen), nil
}

// Forget removes key and every association between it and its tracked tags.
// Forgetting a key that is not tracked is not an error.
func (t *MemoryTracker) Forget(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	for tag := range t.keyToTags[key] {
		t.untrackKeyFromTagLocked(tag, key)
	}
	delete(t.keyToTags, key)

	return nil
}

// ForgetTags removes every one of tags, and the association between each of them and
// the keys tracked under it. A key that remains tracked under some other tag keeps
// that association; a key left with no tags at all is dropped from the index
// entirely. An empty tags, or a tag with no tracked keys, is not an error.
func (t *MemoryTracker) ForgetTags(ctx context.Context, tags []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	for _, tag := range tags {
		entry, ok := t.tagToKeys[tag]
		if !ok {
			continue
		}
		for key := range entry.nodes {
			t.untagKeyLocked(key, tag)
		}
		delete(t.tagToKeys, tag)
	}

	return nil
}

// Tags returns the de-duplicated, sorted set of tags currently recorded for key. A
// key that is not tracked returns an empty slice, not an error.
func (t *MemoryTracker) Tags(ctx context.Context, key string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	return sortedKeys(t.keyToTags[key]), nil
}

// Clear removes every tracked key and tag. It does not reset Stats().Dropped, which
// is a cumulative counter.
func (t *MemoryTracker) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.keyToTags = make(map[string]map[string]struct{})
	t.tagToKeys = make(map[string]*tagEntry)

	return nil
}

// Stats returns a snapshot of the tracker's current size and cumulative eviction
// count.
func (t *MemoryTracker) Stats() Stats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return Stats{
		Keys:    uint64(len(t.keyToTags)),
		Tags:    uint64(len(t.tagToKeys)),
		Dropped: t.dropped,
	}
}

// trackKeyUnderTagLocked adds key to tag's FIFO key list, creating the tag's entry
// if needed. If that puts the tag over MaxKeysPerTag, it drops the tag's oldest key
// — which may or may not be key itself — and cleans up that key's entry in
// t.keyToTags to match, incrementing t.dropped. Callers must hold t.mu for writing
// and must separately record key's membership in tag on the t.keyToTags side.
func (t *MemoryTracker) trackKeyUnderTagLocked(tag, key string) {
	entry, ok := t.tagToKeys[tag]
	if !ok {
		entry = &tagEntry{nodes: make(map[string]*keyNode)}
		t.tagToKeys[tag] = entry
	}
	entry.pushBack(key)

	if t.MaxKeysPerTag <= 0 {
		return
	}
	for len(entry.nodes) > t.MaxKeysPerTag {
		oldest := entry.head.key
		entry.unlink(entry.head)
		t.untagKeyLocked(oldest, tag)
		t.dropped++
	}
}

// untrackKeyFromTagLocked removes key from tag's FIFO key list, dropping the tag's
// entry entirely once it has no keys left. It touches only the tag side of the
// index; callers must separately update key's entry on the t.keyToTags side. It is a
// no-op if tag or key within it is not present. Callers must hold t.mu for writing.
func (t *MemoryTracker) untrackKeyFromTagLocked(tag, key string) {
	entry, ok := t.tagToKeys[tag]
	if !ok {
		return
	}
	node, ok := entry.nodes[key]
	if !ok {
		return
	}
	entry.unlink(node)
	if len(entry.nodes) == 0 {
		delete(t.tagToKeys, tag)
	}
}

// untagKeyLocked removes tag from key's entry on the t.keyToTags side, dropping
// key's entry entirely once it has no tags left. It touches only the key side of the
// index; callers must separately update tag's entry on the t.tagToKeys side. Callers
// must hold t.mu for writing.
func (t *MemoryTracker) untagKeyLocked(key, tag string) {
	tags, ok := t.keyToTags[key]
	if !ok {
		return
	}
	delete(tags, tag)
	if len(tags) == 0 {
		delete(t.keyToTags, key)
	}
}

// sortedKeys returns set's members as a de-duplicated, sorted slice. It is used by
// both Resolve and Tags so their output is deterministic regardless of Go's
// randomized map iteration order.
func sortedKeys(set map[string]struct{}) []string {
	result := make([]string, 0, len(set))
	for k := range set {
		result = append(result, k)
	}
	sort.Strings(result)
	return result
}
