// Package datacache keeps the data fragments fetch, across renders, so that twenty
// pages showing one author ask the upstream for that author once.
//
// The page cache stores what a render produced; this stores what renders are made
// from. It is bounded, keyed by the application's own keys, and marked stale by the
// same dependency tags the page cache is invalidated by. A stale value is still
// served while one fetch in the background replaces it: an upstream that is down
// or slow costs freshness, never the page.
package datacache

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/types"
)

// ErrFetchPanicked is what a caller waiting on a fetch is told when the fetch
// panicked. The panic itself goes to the caller whose fetch it was.
var ErrFetchPanicked = errors.New("collage: data fetch panicked")

// DefaultMaxEntries bounds a store given no bound of its own.
const DefaultMaxEntries = 10000

// RefreshTimeout bounds a background refresh. It is not a request's timeout:
// nobody waits on a refresh, and an upstream slower than a page's patience may
// still answer in time for the next one.
const RefreshTimeout = 30 * time.Second

// Options configures a Store.
type Options struct {
	// MaxEntries bounds the store: 0 means DefaultMaxEntries and a negative
	// value means unbounded.
	MaxEntries int
	// Logger receives failed refreshes. Nil means slog.Default().
	Logger *slog.Logger
	// Metrics receives a CacheStale event for each stale value served. Nil
	// means none.
	Metrics observability.Metrics
}

// Store is a bounded, tag-invalidated cache of fetched values. It is safe for
// concurrent use.
type Store struct {
	mu       sync.Mutex
	max      int
	entries  map[string]*list.Element
	order    *list.List // front is most recently used
	byTag    map[string]map[string]struct{}
	inflight map[string]*call
	// epoch counts invalidations and purges. A fetch that started before one
	// brought back what it was meant to replace: it is stored, but stale.
	epoch uint64
	// purges counts purges. A fetch that started before one stores nothing,
	// since the application asked for the value to be gone.
	purges         uint64
	now            func() time.Time
	logger         *slog.Logger
	metrics        observability.Metrics
	refreshTimeout time.Duration
	// refreshes counts background refreshes still running.
	refreshes sync.WaitGroup
	// disk is nil for a store kept in memory only.
	disk *disk
	// pending holds the disk writes the current critical section made; unlock
	// performs them after releasing mu.
	pending []diskOp
	// gens holds each key's latest change, numbered from seq. A disk write
	// carries the number of the change it records and is skipped once a later
	// change exists, so writes that finish out of order cannot leave an older
	// value on disk.
	gens map[string]uint64
	seq  uint64
	// diskMu orders disk writes. It is never taken while mu is held.
	diskMu sync.Mutex
	// unencodable remembers the key names already logged as not persistable;
	// guarded by diskMu.
	unencodable map[string]bool
}

type entry struct {
	key   string
	value types.CacheValue
	// raw is the encoded value as read from disk, decoded on first load. While
	// it is set, value is nil.
	raw     []byte
	expires time.Time
	tags    []string
	// stale is set by an invalidation; an entry past expires is stale as well.
	stale      bool
	staleSince time.Time
}

type call struct {
	done  chan struct{}
	value types.CacheValue
	err   error
	// abandoned reports a fetch that failed because the caller that started it
	// went away: an answer about that caller, not about the key.
	abandoned bool
}

// New returns an empty store kept in memory only.
func New(opts Options) *Store {
	maxEntries := opts.MaxEntries
	if maxEntries == 0 {
		maxEntries = DefaultMaxEntries
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{
		max:            maxEntries,
		entries:        make(map[string]*list.Element),
		order:          list.New(),
		byTag:          make(map[string]map[string]struct{}),
		inflight:       make(map[string]*call),
		now:            time.Now,
		logger:         logger,
		metrics:        observability.MetricsOrNoop(opts.Metrics),
		refreshTimeout: RefreshTimeout,
	}
}

// Load returns the value stored under key, fetching it when there is none.
// Concurrent loads of one key share one fetch. A ttl of zero or less keeps the
// value fresh until its tags are invalidated.
//
// A value past its ttl, or one whose tags were invalidated, is stale: Load
// returns it at once with stale true, and starts one fetch in the background to
// replace it. That fetch is not the caller's: it runs detached from ctx, under
// RefreshTimeout, so a reader who leaves does not cancel it. If it fails the
// stale value stays and is served again; if it reports types.ErrNotFound the
// value is dropped, so the next Load fetches and returns that error.
//
// With no value at all there is nothing to serve meanwhile, and Load waits for
// the fetch, whose error, if any, goes to every caller waiting on it and is
// never stored. A ctx marked with types.WantsFreshData waits the same way on a
// stale value, and gets the upstream's error rather than the value.
//
// decode is not used by a store kept in memory.
func (s *Store) Load(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (types.CacheValue, error), decode func([]byte) (types.CacheValue, error)) (types.CacheValue, bool, error) {
	for {
		value, stale, err, again := s.load(ctx, key, ttl, tags, fetch, decode)
		if !again {
			return value, stale, err
		}
	}
}

// load is one attempt at Load. again reports that it waited on a fetch its
// starter abandoned, and the caller should try once more.
func (s *Store) load(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (types.CacheValue, error), decode func([]byte) (types.CacheValue, error)) (value types.CacheValue, stale bool, err error, again bool) {
	s.mu.Lock()
	if element, ok := s.entries[key]; ok && element.Value.(*entry).raw != nil {
		// Decoded outside the lock: a large value must not stall every other
		// key. Whoever finishes first installs it.
		stored := element.Value.(*entry)
		raw := stored.raw
		s.mu.Unlock()
		decoded, decodeErr := guardedDecode(decode, raw)
		s.mu.Lock()
		if current, still := s.entries[key]; still && current == element && stored.raw != nil {
			if decodeErr != nil {
				// The type changed between builds in a way its identity
				// did not catch. A miss, never the page's error.
				s.logger.Warn("collage: dropping a stored value that no longer decodes", "key", keyName(key), "err", decodeErr)
				s.removeLocked(element)
				s.unlock()
				s.mu.Lock()
			} else {
				stored.value, stored.raw = decoded, nil
			}
		}
	}
	if element, ok := s.entries[key]; ok {
		stored := element.Value.(*entry)
		if !s.staleLocked(stored) {
			s.order.MoveToFront(element)
			s.mu.Unlock()
			return stored.value, false, nil, false
		}
		if !types.WantsFreshData(ctx) {
			s.order.MoveToFront(element)
			s.refreshLocked(ctx, key, ttl, tags, fetch)
			value := stored.value
			s.mu.Unlock()
			s.metrics.CacheEvent(ctx, observability.CacheStale, keyName(key))
			return value, true, nil, false
		}
		// Fresh data wanted: wait below like a miss. The stale value stays
		// until a successful fetch replaces it.
	}
	if running, ok := s.inflight[key]; ok {
		s.mu.Unlock()
		select {
		case <-running.done:
			// Not the starter's cancellation: one reader disconnecting, on
			// purpose or not, would fail every reader who shared its fetch.
			if running.abandoned && ctx.Err() == nil {
				return nil, false, nil, true
			}
			return running.value, false, running.err, false
		case <-ctx.Done():
			return nil, false, ctx.Err(), false
		}
	}
	started := &call{done: make(chan struct{})}
	s.inflight[key] = started
	epoch, purges := s.epoch, s.purges
	s.mu.Unlock()

	// A fetch that panics still gives the key up and answers its waiters. The
	// render recovers the panic further up, and without this the key would
	// stay in flight for good: every later load waits on it and none fetches.
	finished := false
	defer func() {
		if finished {
			return
		}
		started.err = ErrFetchPanicked
		s.mu.Lock()
		delete(s.inflight, key)
		s.mu.Unlock()
		close(started.done)
	}()
	value, err = fetch(ctx)
	finished = true
	started.value, started.err = value, err
	started.abandoned = err != nil && ctx.Err() != nil

	s.mu.Lock()
	delete(s.inflight, key)
	s.settleLocked(key, value, err, ttl, tags, epoch, purges)
	// Waiters are answered before the disk is written: memory is settled, and
	// a slow disk should cost only the caller that fetched.
	ops := s.release()
	close(started.done)
	s.flush(ops)
	return value, false, err, false
}

// refreshLocked starts the background fetch replacing key's stale value,
// unless one is already running. It must be called with s.mu held.
func (s *Store) refreshLocked(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (types.CacheValue, error)) {
	if _, running := s.inflight[key]; running {
		return
	}
	started := &call{done: make(chan struct{})}
	s.inflight[key] = started
	epoch, purges := s.epoch, s.purges
	s.refreshes.Add(1)
	go s.refresh(context.WithoutCancel(ctx), key, ttl, tags, fetch, started, epoch, purges)
}

func (s *Store) refresh(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (types.CacheValue, error), started *call, epoch, purges uint64) {
	defer s.refreshes.Done()
	ctx, cancel := context.WithTimeout(ctx, s.refreshTimeout)
	defer cancel()
	value, err := guarded(ctx, fetch)
	started.value, started.err = value, err

	s.mu.Lock()
	delete(s.inflight, key)
	var staleFor time.Duration
	if element, ok := s.entries[key]; ok && err != nil {
		staleFor = s.now().Sub(s.staleSinceLocked(element.Value.(*entry)))
	}
	s.settleLocked(key, value, err, ttl, tags, epoch, purges)
	ops := s.release()
	close(started.done)
	s.flush(ops)

	if err != nil && !errors.Is(err, types.ErrNotFound) {
		s.logger.Warn("collage: serving stale data, its refresh failed",
			"key", keyName(key), "stale_for", staleFor, "err", err)
	}
}

// settleLocked records what a fetch brought back: a value is stored, stale if
// an invalidation overtook the fetch and not at all if a purge did; not found
// drops what was there; any other error leaves it. It must be called with s.mu
// held.
func (s *Store) settleLocked(key string, value types.CacheValue, err error, ttl time.Duration, tags []string, epoch, purges uint64) {
	switch {
	case err == nil:
		if purges == s.purges {
			s.storeLocked(key, value, ttl, tags, epoch != s.epoch)
		}
	case errors.Is(err, types.ErrNotFound):
		if element, ok := s.entries[key]; ok {
			s.removeLocked(element)
		}
	}
}

// guarded runs fetch, turning a panic into ErrFetchPanicked: a refresh runs on
// its own goroutine, where nothing above it would recover one.
func guarded(ctx context.Context, fetch func(context.Context) (types.CacheValue, error)) (value types.CacheValue, err error) {
	defer func() {
		if r := recover(); r != nil {
			value, err = nil, fmt.Errorf("%w: %v", ErrFetchPanicked, r)
		}
	}()
	return fetch(ctx)
}

// Invalidate marks every value stored under any of tags stale and returns how
// many it marked. They are still served until a fetch replaces them. A fetch
// running when it is called stores what it brings back as stale.
func (s *Store) Invalidate(tags []string) int {
	s.mu.Lock()
	defer s.unlock()
	s.epoch++
	now := s.now()
	marked := 0
	for _, tag := range tags {
		for key := range s.byTag[tag] {
			if element, ok := s.entries[key]; ok {
				s.markStaleLocked(element.Value.(*entry), now)
				marked++
			}
		}
	}
	return marked
}

// Purge removes every value stored under any of tags and returns how many it
// removed. A fetch running when it is called stores nothing.
func (s *Store) Purge(tags []string) int {
	s.mu.Lock()
	defer s.unlock()
	s.epoch++
	s.purges++
	removed := 0
	for _, tag := range tags {
		for key := range s.byTag[tag] {
			if element, ok := s.entries[key]; ok {
				s.removeLocked(element)
				removed++
			}
		}
	}
	return removed
}

// Len reports how many values are stored, stale ones included.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func (s *Store) staleLocked(e *entry) bool {
	return e.stale || (!e.expires.IsZero() && !s.now().Before(e.expires))
}

func (s *Store) staleSinceLocked(e *entry) time.Time {
	if e.stale {
		return e.staleSince
	}
	return e.expires
}

func (s *Store) markStaleLocked(e *entry, now time.Time) {
	if !e.stale {
		e.stale, e.staleSince = true, now
		s.changedLocked(e.key, e)
	}
}

// storeLocked must be called with s.mu held.
func (s *Store) storeLocked(key string, value types.CacheValue, ttl time.Duration, tags []string, stale bool) {
	if element, ok := s.entries[key]; ok {
		s.removeLocked(element)
	}
	stored := &entry{key: key, value: value, tags: append([]string(nil), tags...)}
	now := s.now()
	if ttl > 0 {
		stored.expires = now.Add(ttl)
	}
	if stale {
		s.markStaleLocked(stored, now)
	}
	s.entries[key] = s.order.PushFront(stored)
	s.indexLocked(stored)
	s.changedLocked(key, stored)
	for s.max > 0 && len(s.entries) > s.max {
		s.removeLocked(s.order.Back())
	}
}

// indexLocked must be called with s.mu held.
func (s *Store) indexLocked(e *entry) {
	for _, tag := range e.tags {
		keys, ok := s.byTag[tag]
		if !ok {
			keys = make(map[string]struct{})
			s.byTag[tag] = keys
		}
		keys[e.key] = struct{}{}
	}
}

// removeLocked must be called with s.mu held.
func (s *Store) removeLocked(element *list.Element) {
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
	s.changedLocked(stored.key, nil)
}

// keyName is the part of a store key the application chose, for logs and
// metrics: types.storeKey appends the type after a NUL.
func keyName(key string) string {
	name, _, _ := strings.Cut(key, "\x00")
	return name
}

// diskOp records one change to an entry for the disk: its whole current state,
// or its removal.
type diskOp struct {
	key    string
	gen    uint64
	remove bool
	header header
	value  types.CacheValue
	raw    []byte
}

// changedLocked records that key changed and, for a store on disk, queues the
// write that records it. e is nil when the entry was removed. It must be called
// with s.mu held.
func (s *Store) changedLocked(key string, e *entry) {
	if s.disk == nil {
		return
	}
	s.seq++
	s.gens[key] = s.seq
	op := diskOp{key: key, gen: s.seq, remove: e == nil}
	if e != nil {
		op.header = header{
			Version: formatVersion, Key: key, Tags: e.tags, Expires: e.expires,
			Stale: e.stale, StaleSince: e.staleSince, Written: s.now(),
		}
		op.value, op.raw = e.value, e.raw
	}
	s.pending = append(s.pending, op)
}

// unlock releases s.mu and writes what the critical section changed to disk.
func (s *Store) unlock() {
	s.flush(s.release())
}

// release releases s.mu and returns the disk writes the critical section
// queued, for a caller with something to do between the two.
func (s *Store) release() []diskOp {
	ops := s.pending
	s.pending = nil
	s.mu.Unlock()
	return ops
}

func (s *Store) flush(ops []diskOp) {
	if len(ops) == 0 {
		return
	}
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	for _, op := range ops {
		s.mu.Lock()
		current := s.gens[op.key] == op.gen
		s.mu.Unlock()
		if !current {
			continue
		}
		var err error
		if op.remove {
			err = s.disk.remove(op.key)
			s.mu.Lock()
			if s.gens[op.key] == op.gen {
				delete(s.gens, op.key)
			}
			s.mu.Unlock()
		} else {
			err = s.writeOp(op)
		}
		if err != nil {
			s.logger.Warn("collage: data cache disk write failed", "key", keyName(op.key), "err", err)
		}
	}
}

// writeOp writes op's entry. A value that cannot be encoded is kept in memory
// only, and an older copy of it on disk is removed so a restart does not
// bring it back.
func (s *Store) writeOp(op diskOp) error {
	payload := op.raw
	if payload == nil {
		encoded, err := encode(op.value)
		if err != nil {
			if name := keyName(op.key); !s.unencodable[name] {
				s.unencodable[name] = true
				s.logger.Warn("collage: data cache value kept in memory only", "key", name, "err", err)
			}
			return s.disk.remove(op.key)
		}
		payload = encoded
	}
	return s.disk.write(op.header, payload)
}

// encode runs value's Encode, turning a panic into an error: it runs after a
// fetch has settled, on a refresh's goroutine or before a fetch's caller
// returns, where a panic would crash the process or lose the rest of a flush.
// A nil value has nothing to write and is reported the same way.
func encode(value types.CacheValue) (encoded []byte, err error) {
	if value == nil {
		return nil, errors.New("nil value")
	}
	defer func() {
		if r := recover(); r != nil {
			encoded, err = nil, fmt.Errorf("encode panicked: %v", r)
		}
	}()
	return value.Encode()
}

// guardedDecode runs decode, turning a panic, or a value it does not return,
// into an error: either way what is on disk cannot be served, and the caller
// drops it and fetches rather than failing that key on every load.
func guardedDecode(decode func([]byte) (types.CacheValue, error), raw []byte) (decoded types.CacheValue, err error) {
	defer func() {
		if r := recover(); r != nil {
			decoded, err = nil, fmt.Errorf("decode panicked: %v", r)
		}
	}()
	decoded, err = decode(raw)
	if err == nil && decoded == nil {
		err = errors.New("decode returned no value")
	}
	return decoded, err
}

// Open returns a store that keeps its values in dir as well as in memory, and
// starts with what dir already holds: the newest entries, up to the bound, the
// rest removed. Their values are decoded on first load.
func Open(dir string, opts Options) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("collage: data cache directory: %w", err)
	}
	s := New(opts)
	s.disk = &disk{dir: dir}
	s.gens = make(map[string]uint64)
	s.unencodable = make(map[string]bool)
	records, err := s.disk.load()
	if err != nil {
		return nil, fmt.Errorf("collage: data cache directory: %w", err)
	}
	for _, rec := range records {
		if s.max > 0 && len(s.entries) >= s.max {
			if err := s.disk.remove(rec.header.Key); err != nil {
				s.logger.Warn("collage: data cache disk write failed", "key", keyName(rec.header.Key), "err", err)
			}
			continue
		}
		raw := rec.payload
		if raw == nil {
			raw = []byte{}
		}
		stored := &entry{
			key: rec.header.Key, raw: raw, expires: rec.header.Expires, tags: rec.header.Tags,
			stale: rec.header.Stale, staleSince: rec.header.StaleSince,
		}
		s.entries[stored.key] = s.order.PushBack(stored)
		s.indexLocked(stored)
	}
	return s, nil
}
