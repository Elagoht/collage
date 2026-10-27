package core

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/Elagoht/collage/internal/render"
)

// fragmentMemo keeps RenderFragment's Shared renders, so a change pushed to many
// readers is rendered once rather than once per reader.
//
// A plugin pushing a fragment calls RenderFragment for each reader, so that each
// reader's middleware and guards are asked whether it may still see it. When the
// render is Shared it is made from a request with nothing of the reader's in
// it, so every reader allowed to see it would get the same bytes: the memo hands
// a reader who passed the chain and the guards the render another reader's
// call made, and the token and every PersonaliseHook then run on a copy of it
// for this reader, as they would on a fresh render.
//
// An entry is dropped when a tag it depends on is invalidated or purged, and the
// path and host tags of its URL count among them; past its age, which is the
// page's cache lifetime for a page cached for everyone, and memoWindow for a
// page rendered per request, whose Shared handlers may change without an
// invalidation; and when the memo is full, least recently used first. A render
// that an invalidation overlapped is not kept, and readers arriving during a
// render wait for it rather than make their own.
type fragmentMemo struct {
	mu sync.Mutex
	// epoch counts the invalidations: a render begun in one is not kept in a
	// later one, since what it read may be what was invalidated.
	epoch   uint64
	entries map[string]*memoEntry
	// byTag finds the entries a tag reaches.
	byTag map[string]map[*memoEntry]struct{}
	// head is the most recently used entry, tail the least; the list is the
	// entries' own prev and next.
	head, tail *memoEntry
	bytes      int64
	// flights are the renders under way, by key, begun in this epoch.
	flights map[string]*memoFlight

	maxEntries int
	maxBytes   int64
	now        func() time.Time
}

// memoWindow is how long a Shared render of a page rendered per request is
// reused: long enough that one change pushed to every reader is one render, and
// short enough that a handler declared Shared that changes on its own — a
// count, a clock — is not frozen for a reader subscribing later.
const memoWindow = time.Second

const (
	defaultMemoEntries = 4096
	defaultMemoBytes   = 32 << 20
)

type memoEntry struct {
	key        string
	result     *render.FragmentResult
	tags       []string
	expires    time.Time // zero: until invalidated
	size       int64
	prev, next *memoEntry
}

type memoFlight struct {
	done   chan struct{}
	result *render.FragmentResult
	err    error
}

func newFragmentMemo(maxBytes int64) *fragmentMemo {
	if maxBytes <= 0 || maxBytes > defaultMemoBytes {
		maxBytes = defaultMemoBytes
	}
	return &fragmentMemo{
		entries:    make(map[string]*memoEntry),
		byTag:      make(map[string]map[*memoEntry]struct{}),
		flights:    make(map[string]*memoFlight),
		maxEntries: defaultMemoEntries,
		maxBytes:   maxBytes,
		now:        time.Now,
	}
}

// get returns a copy of the render kept under key, or of the one under way, or
// makes it with renderFn and keeps it for age (zero: until invalidated) under tags
// as well as its own. A reader waiting on another's render stops waiting when
// ctx ends. renderFn runs detached from ctx: others may be waiting on it.
func (m *fragmentMemo) get(ctx context.Context, key string, age time.Duration, tags []string,
	renderFn func(context.Context) (*render.FragmentResult, error)) (*render.FragmentResult, error) {
	m.mu.Lock()
	if e := m.entries[key]; e != nil {
		if e.expires.IsZero() || m.now().Before(e.expires) {
			m.toFront(e)
			out := cloneResult(e.result)
			m.mu.Unlock()
			return out, nil
		}
		m.remove(e)
	}
	if f := m.flights[key]; f != nil {
		m.mu.Unlock()
		select {
		case <-f.done:
			if f.err != nil {
				return nil, f.err
			}
			return cloneResult(f.result), nil
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
	f := &memoFlight{done: make(chan struct{})}
	m.flights[key] = f
	epoch := m.epoch
	m.mu.Unlock()

	// Closed whatever renderFn does, a panic included, so no follower waits forever.
	defer func() {
		m.mu.Lock()
		if m.flights[key] == f {
			delete(m.flights, key)
		}
		if f.err == nil && f.result != nil && m.epoch == epoch {
			m.store(key, f.result, tags, age)
		}
		m.mu.Unlock()
		close(f.done)
	}()
	f.err = errFlightAbandoned
	f.result, f.err = renderFn(context.WithoutCancel(ctx))
	if f.err != nil {
		return nil, f.err
	}
	return cloneResult(f.result), nil
}

// errFlightAbandoned is what a reader waiting on a render that panicked gets.
var errFlightAbandoned = errors.New("collage: the shared fragment render this reader waited on did not finish")

// drop forgets every entry tags reach, and ends the epoch: a render under way
// is not kept, and a reader arriving now does not wait on it.
func (m *fragmentMemo) drop(tags []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.epoch++
	clear(m.flights)
	for _, tag := range tags {
		for e := range m.byTag[tag] {
			m.remove(e)
		}
	}
}

// store keeps result under key; m.mu is held.
func (m *fragmentMemo) store(key string, result *render.FragmentResult, tags []string, age time.Duration) {
	if old := m.entries[key]; old != nil {
		m.remove(old)
	}
	e := &memoEntry{key: key, result: cloneResult(result), size: resultSize(result)}
	e.tags = slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(result.DependencyTags), tags...))))
	if age > 0 {
		e.expires = m.now().Add(age)
	}
	if e.size > m.maxBytes {
		return
	}
	m.entries[key] = e
	for _, tag := range e.tags {
		set := m.byTag[tag]
		if set == nil {
			set = make(map[*memoEntry]struct{})
			m.byTag[tag] = set
		}
		set[e] = struct{}{}
	}
	m.bytes += e.size
	m.pushFront(e)
	for (len(m.entries) > m.maxEntries || m.bytes > m.maxBytes) && m.tail != nil {
		m.remove(m.tail)
	}
}

// remove forgets e; m.mu is held.
func (m *fragmentMemo) remove(e *memoEntry) {
	if m.entries[e.key] != e {
		return
	}
	delete(m.entries, e.key)
	for _, tag := range e.tags {
		if set := m.byTag[tag]; set != nil {
			delete(set, e)
			if len(set) == 0 {
				delete(m.byTag, tag)
			}
		}
	}
	m.bytes -= e.size
	m.unlink(e)
}

func (m *fragmentMemo) pushFront(e *memoEntry) {
	e.prev, e.next = nil, m.head
	if m.head != nil {
		m.head.prev = e
	}
	m.head = e
	if m.tail == nil {
		m.tail = e
	}
}

func (m *fragmentMemo) unlink(e *memoEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		m.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		m.tail = e.prev
	}
	e.prev, e.next = nil, nil
}

func (m *fragmentMemo) toFront(e *memoEntry) {
	if m.head == e {
		return
	}
	m.unlink(e)
	m.pushFront(e)
}

// cloneResult copies r deeply enough that nothing done to the copy — a token
// written into its markup, a hook rewriting a hoisted item — reaches r.
func cloneResult(r *render.FragmentResult) *render.FragmentResult {
	return &render.FragmentResult{
		HTML:           slices.Clone(r.HTML),
		Head:           slices.Clone(r.Head),
		DependencyTags: slices.Clone(r.DependencyTags),
	}
}

func resultSize(r *render.FragmentResult) int64 {
	size := int64(len(r.HTML))
	for _, item := range r.Head {
		size += int64(len(item.Area) + len(item.Key) + len(item.HTML))
	}
	for _, tag := range r.DependencyTags {
		size += int64(len(tag))
	}
	return size
}
