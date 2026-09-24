// Package datacache keeps the data fragments fetch, across renders, so that twenty
// pages showing one author ask the upstream for that author once.
//
// The page cache stores what a render produced; this stores what renders are made
// from. It is in-process and bounded, keyed by the application's own keys, and
// invalidated by the same dependency tags the page cache is, so one
// InvalidateTags drops a record and every page built from it together.
package datacache

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// DefaultMaxEntries bounds a store given no bound of its own.
const DefaultMaxEntries = 10000

// Store is a bounded, tag-invalidated cache of fetched values. It is safe for
// concurrent use.
type Store struct {
	mu       sync.Mutex
	max      int
	entries  map[string]*list.Element
	order    *list.List // front is most recently used
	byTag    map[string]map[string]struct{}
	inflight map[string]*call
	// epoch counts invalidations. A fetch that started before one finished
	// with data the invalidation was meant to replace, so it is handed to its
	// callers and not stored.
	epoch uint64
	now   func() time.Time
}

type entry struct {
	key     string
	value   any // any: the store holds every caller's type; Cached asserts it back
	expires time.Time
	tags    []string
}

type call struct {
	done  chan struct{}
	value any // any: see entry.value
	err   error
}

// New returns a store holding at most maxEntries values: 0 means DefaultMaxEntries
// and a negative value means unbounded.
func New(maxEntries int) *Store {
	if maxEntries == 0 {
		maxEntries = DefaultMaxEntries
	}
	return &Store{
		max:      maxEntries,
		entries:  make(map[string]*list.Element),
		order:    list.New(),
		byTag:    make(map[string]map[string]struct{}),
		inflight: make(map[string]*call),
		now:      time.Now,
	}
}

// Load returns the value stored under key, fetching it when there is none or it has
// expired. Concurrent loads of one key share one fetch. A ttl of zero or less keeps
// the value until its tags are invalidated or it is evicted; an error is returned to
// every caller waiting on that fetch and never stored.
//
// The fetch runs under the context of the caller that started it. A caller whose own
// context ends while it waits stops waiting.
func (s *Store) Load(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (any, error)) (any, error) { // any: see entry.value
	s.mu.Lock()
	if element, ok := s.entries[key]; ok {
		stored := element.Value.(*entry)
		if stored.expires.IsZero() || s.now().Before(stored.expires) {
			s.order.MoveToFront(element)
			s.mu.Unlock()
			return stored.value, nil
		}
		s.remove(element)
	}
	if running, ok := s.inflight[key]; ok {
		s.mu.Unlock()
		select {
		case <-running.done:
			return running.value, running.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	started := &call{done: make(chan struct{})}
	s.inflight[key] = started
	epoch := s.epoch
	s.mu.Unlock()

	value, err := fetch(ctx)
	started.value, started.err = value, err

	s.mu.Lock()
	delete(s.inflight, key)
	if err == nil && epoch == s.epoch {
		s.store(key, value, ttl, tags)
	}
	s.mu.Unlock()
	close(started.done)
	return value, err
}

// Invalidate drops every value stored under any of tags and returns how many it
// dropped. A fetch still running when it is called is not stored when it finishes.
func (s *Store) Invalidate(tags []string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	dropped := 0
	for _, tag := range tags {
		for key := range s.byTag[tag] {
			if element, ok := s.entries[key]; ok {
				s.remove(element)
				dropped++
			}
		}
	}
	return dropped
}

// Len reports how many values are stored.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// store must be called with s.mu held.
func (s *Store) store(key string, value any, ttl time.Duration, tags []string) { // any: see entry.value
	if element, ok := s.entries[key]; ok {
		s.remove(element)
	}
	stored := &entry{key: key, value: value, tags: append([]string(nil), tags...)}
	if ttl > 0 {
		stored.expires = s.now().Add(ttl)
	}
	s.entries[key] = s.order.PushFront(stored)
	for _, tag := range stored.tags {
		keys, ok := s.byTag[tag]
		if !ok {
			keys = make(map[string]struct{})
			s.byTag[tag] = keys
		}
		keys[key] = struct{}{}
	}
	for s.max > 0 && len(s.entries) > s.max {
		s.remove(s.order.Back())
	}
}

// remove must be called with s.mu held.
func (s *Store) remove(element *list.Element) {
	stored := element.Value.(*entry)
	s.order.Remove(element)
	delete(s.entries, stored.key)
	for _, tag := range stored.tags {
		if keys, ok := s.byTag[tag]; ok {
			delete(keys, stored.key)
			if len(keys) == 0 {
				delete(s.byTag, tag)
			}
		}
	}
}
