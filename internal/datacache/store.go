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

	"github.com/Elagoht/collage/internal/cache"
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

// After a refresh fails, the key cools down before another is started: an
// upstream that is down is asked once a second, then every two, four, … up to
// once a minute, not once for every request that serves the stale value.
const (
	retryBackoffMin = time.Second
	retryBackoffMax = time.Minute
)

// retryBackoff is the cooldown after failures consecutive failed refreshes.
func retryBackoff(failures int) time.Duration {
	backoff := retryBackoffMin
	for i := 1; i < failures && backoff < retryBackoffMax; i++ {
		backoff *= 2
	}
	return min(backoff, retryBackoffMax)
}

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
	// Backend, when set, holds the values instead of the store: a store a fleet
	// shares, so one instance's invalidation is every instance's. MaxEntries
	// does not apply; the backend bounds itself.
	Backend Backend
	// Health, with a Backend, is where the backend's failures are reported: the
	// App's one SharedHealth, which the page paths report into as well, so the
	// store failing under both is one warning per window. Nil gives the store
	// one of its own, logging to Logger.
	Health *cache.SharedHealth
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
	// closing is set by Close: no refresh starts after it, so the wait for
	// the running ones ends.
	closing   bool
	closeOnce sync.Once
	closeErr  error
	// disk is nil for a store kept in memory only.
	disk *disk
	// queue holds the disk writes not yet made. One writer goroutine makes
	// them, so a caller that changed many entries — an invalidation marking
	// hundreds stale — returns at memory speed. changed wakes the writer, and
	// whoever waits for the queue to drain; both use mu.
	queue   []diskOp
	writing bool
	changed *sync.Cond
	// stopped is set once the writer is told to stop; changes made after it
	// are kept in memory only.
	stopped    bool
	writerDone chan struct{}
	// gens holds each key's latest change, numbered from seq. A disk write
	// carries the number of the change it records and is skipped once a later
	// change exists, so writes that finish out of order cannot leave an older
	// value on disk.
	gens map[string]uint64
	seq  uint64
	// diskMu orders disk writes. It is never taken while mu is held.
	diskMu sync.Mutex
	// unencodable remembers the types already logged as not persistable, so
	// the log, and the map, grow with the application's types, not its keys;
	// guarded by diskMu.
	unencodable map[string]bool
	// backend is nil for a store that keeps its own values. With one, entries
	// stays empty and cooling holds the only per-key state kept in process.
	backend        Backend
	backendTimeout time.Duration
	cooling        map[string]*cooldown
	coolSeq        uint64
	health         *cache.SharedHealth
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
	// failures counts the refreshes that failed in a row, and retryAt is when
	// the next may start. A success replaces the entry, and so clears both.
	failures int
	retryAt  time.Time
}

type call struct {
	done  chan struct{}
	value types.CacheValue
	err   error
	// abandoned reports a fetch that failed because the caller that started it
	// went away: an answer about that caller, not about the key.
	abandoned bool
	// epoch is the store's epoch when the fetch started.
	epoch uint64
	// overtaken reports a fetch whose value was stored stale or not at all:
	// an invalidation or a purge came after it started — in this process, by
	// the store's epoch, or on any instance, by the backend's answer to the
	// write. It is set before done is closed.
	overtaken bool
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
	s := &Store{
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
	if opts.Backend != nil {
		s.backend = opts.Backend
		s.backendTimeout = BackendTimeout
		s.cooling = make(map[string]*cooldown)
		s.unencodable = make(map[string]bool)
		s.health = opts.Health
		if s.health == nil {
			// Read through s.now at each call, so a test that replaces the
			// store's clock moves the window too.
			s.health = cache.NewSharedHealth(logger, func() time.Time { return s.now() })
		}
	}
	return s
}

// Load returns the value stored under key, fetching it when there is none.
// Concurrent loads of one key share one fetch. A ttl of zero or less keeps the
// value fresh until its tags are invalidated.
//
// A value past its ttl, or one whose tags were invalidated, is stale: Load
// returns it at once with stale true, and starts one fetch in the background to
// replace it. That fetch is not the caller's: it runs detached from ctx, under
// RefreshTimeout, so a reader who leaves does not cancel it. If it fails the
// stale value stays and is served again, and the key cools down: no refresh
// starts for it for a second, doubling with each further failure up to a
// minute, and a success ends it. If it reports types.ErrNotFound the value is
// dropped, so the next Load fetches and returns that error.
//
// With no value at all there is nothing to serve meanwhile, and Load waits for
// the fetch, whose error, if any, goes to every caller waiting on it and is
// never stored. A ctx marked with types.WantsFreshData waits the same way on a
// stale value, cooling down or not, and gets the upstream's error rather than
// the value. It shares a running fetch only if no invalidation came after that
// fetch started. Any other load that shares a fetch an invalidation overtook
// gets its value stale, as the store keeps it.
//
// decode is not used by a store kept in memory. A store with a Backend keeps
// nothing in process but the fetches running and the cooldowns: each Load reads
// the backend, and a backend that fails is a miss, never the caller's error.
func (s *Store) Load(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (types.CacheValue, error), decode func([]byte) (types.CacheValue, error)) (types.CacheValue, bool, error) {
	for {
		value, stale, err, again := s.load(ctx, key, ttl, tags, fetch, decode)
		if !again {
			return value, stale, err
		}
	}
}

// load is one attempt at Load. again reports that it waited on a fetch whose
// answer is not its own — one its starter abandoned, or, for fresh data, one an
// invalidation overtook — and the caller should try once more.
func (s *Store) load(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (types.CacheValue, error), decode func([]byte) (types.CacheValue, error)) (value types.CacheValue, stale bool, err error, again bool) {
	if s.backend != nil {
		return s.loadShared(ctx, key, ttl, tags, fetch, decode)
	}
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
			if !s.now().Before(stored.retryAt) {
				s.refreshLocked(ctx, key, ttl, tags, fetch, stored)
			}
			value := stored.value
			s.mu.Unlock()
			s.metrics.CacheEvent(ctx, observability.CacheStale, keyName(key))
			return value, true, nil, false
		}
		// Fresh data wanted: wait below like a miss. The stale value stays
		// until a successful fetch replaces it.
	}
	if running, ok := s.inflight[key]; ok {
		return s.awaitLocked(ctx, running)
	}
	started := &call{done: make(chan struct{}), epoch: s.epoch}
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
	// A fresh-data load replacing a value whose refreshes failed is the
	// recovery as much as a refresh would have been.
	failures := 0
	if element, ok := s.entries[key]; ok && err == nil {
		failures = element.Value.(*entry).failures
	}
	// Under s.mu and before done is closed, so every load that joined this
	// fetch reads it: they get the value as the store keeps it, stale if an
	// invalidation came after the fetch began.
	started.overtaken = epoch != s.epoch
	s.settleLocked(key, value, err, ttl, tags, epoch, purges)
	s.mu.Unlock()
	close(started.done)
	if failures > 0 {
		s.logger.Info("collage: data refreshed after failures", "key", keyName(key), "failures", failures)
	}
	return value, false, err, false
}

// awaitLocked waits on running, a fetch of the key ctx's load wants, and
// releases s.mu, which it must be called with.
//
// A load that joins a fetch gets its value as the store keeps it: stale when an
// invalidation overtook the fetch, whenever the joiner began. Given as fresh, a
// joiner that began after the invalidation would render undegraded from what
// the invalidation replaced, and its page — whose own mark the invalidation
// predates, so no overlap guard sees it — would be stored fresh: until its TTL,
// or for good on a Static page. Stale, the render is served no-store and not
// stored, as one served a stale value is.
func (s *Store) awaitLocked(ctx context.Context, running *call) (value types.CacheValue, stale bool, err error, again bool) {
	// A fetch that started before an invalidation may have read what it
	// invalidated. That is good enough for a reader served stale anyway, not
	// for one that wants fresh data: it waits the old fetch out, so as not to
	// race it, and then fetches again. With a backend the invalidation may be
	// another instance's, which only the backend's answer to the write tells.
	wantsFresh := types.WantsFreshData(ctx)
	outdated := wantsFresh && running.epoch != s.epoch
	s.mu.Unlock()
	select {
	case <-running.done:
		if (outdated || wantsFresh && running.overtaken) && ctx.Err() == nil {
			return nil, false, nil, true
		}
		// Not the starter's cancellation: one reader disconnecting, on purpose
		// or not, would fail every reader who shared its fetch.
		if running.abandoned && ctx.Err() == nil {
			return nil, false, nil, true
		}
		return running.value, running.err == nil && running.overtaken, running.err, false
	case <-ctx.Done():
		return nil, false, ctx.Err(), false
	}
}

// refreshLocked starts the background fetch replacing target, key's stale
// entry, unless one is already running. It must be called with s.mu held.
func (s *Store) refreshLocked(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (types.CacheValue, error), target *entry) {
	if _, running := s.inflight[key]; running || s.closing {
		return
	}
	started := &call{done: make(chan struct{}), epoch: s.epoch}
	s.inflight[key] = started
	epoch, purges := s.epoch, s.purges
	s.refreshes.Add(1)
	go s.refresh(context.WithoutCancel(ctx), key, ttl, tags, fetch, started, epoch, purges, target)
}

func (s *Store) refresh(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (types.CacheValue, error), started *call, epoch, purges uint64, target *entry) {
	defer s.refreshes.Done()
	ctx, cancel := context.WithTimeout(ctx, s.refreshTimeout)
	defer cancel()
	value, err := guarded(ctx, fetch)
	started.value, started.err = value, err

	s.mu.Lock()
	delete(s.inflight, key)
	// Only the entry this refresh set out to replace cools down or counts as
	// recovered: one a purge removed, or another fetch replaced, meanwhile is
	// not this refresh's to judge.
	element, ok := s.entries[key]
	serving := ok && element.Value.(*entry) == target
	failures := 0
	if serving {
		failures = target.failures
	}
	var staleFor time.Duration
	if serving && err != nil && !errors.Is(err, types.ErrNotFound) {
		now := s.now()
		staleFor = now.Sub(s.staleSinceLocked(target))
		target.failures++
		target.retryAt = now.Add(retryBackoff(target.failures))
	}
	// As in load: what a fresh-data load that joined this refresh is told.
	started.overtaken = epoch != s.epoch
	s.settleLocked(key, value, err, ttl, tags, epoch, purges)
	s.mu.Unlock()
	close(started.done)

	switch {
	case err == nil:
		if failures > 0 {
			s.logger.Info("collage: data refreshed after failures", "key", keyName(key), "failures", failures)
		}
	case errors.Is(err, types.ErrNotFound):
	case serving:
		s.logger.Warn("collage: serving stale data, its refresh failed",
			"key", keyName(key), "stale_for", staleFor, "err", err)
	default:
		// The value is gone — purged or evicted — so nothing is served stale,
		// and the next load fetches and meets the error itself.
		s.logger.Debug("collage: data refresh failed for a value no longer stored", "key", keyName(key), "err", err)
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
//
// With a Backend it marks nothing and returns 0: the values are the backend's,
// and the App drops their tags there, in one call with the pages'. It still
// ends the cooldowns of keys under tags and orders a fresh-data load after it.
func (s *Store) Invalidate(tags []string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	now := s.now()
	marked := 0
	for _, tag := range tags {
		for key := range s.byTag[tag] {
			if element, ok := s.entries[key]; ok {
				stored := element.Value.(*entry)
				s.markStaleLocked(stored, now)
				// An invalidation is news that the upstream is answering, so a
				// key cooling down after failed refreshes tries on its next
				// load. failures stays: one more failure backs off from there.
				stored.retryAt = time.Time{}
				marked++
			}
		}
	}
	s.endCooldownsLocked(tags, false)
	return marked
}

// Purge removes every value stored under any of tags and returns how many it
// removed. A fetch running when it is called stores nothing.
//
// With a Backend it removes nothing and returns 0, as Invalidate does.
func (s *Store) Purge(tags []string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.endCooldownsLocked(tags, true)
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

// keyType is the part of a store key naming its value's type.
func keyType(key string) string {
	_, typ, _ := strings.Cut(key, "\x00")
	return typ
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
// write that records it for the writer. e is nil when the entry was removed. It
// must be called with s.mu held.
func (s *Store) changedLocked(key string, e *entry) {
	if s.disk == nil || s.stopped {
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
	s.queue = append(s.queue, op)
	s.changed.Broadcast()
}

// write is the writer: it takes what is queued, makes the writes with mu
// released, and sleeps until there is more or Close stops it.
func (s *Store) write() {
	defer close(s.writerDone)
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		for len(s.queue) == 0 && !s.stopped {
			s.changed.Wait()
		}
		if s.stopped {
			return
		}
		ops := s.queue
		s.queue, s.writing = nil, true
		s.mu.Unlock()
		s.flush(ops)
		s.mu.Lock()
		s.writing = false
		s.changed.Broadcast()
	}
}

// drainUntil waits until the writer has made every queued write, or ctx ends.
func (s *Store) drainUntil(ctx context.Context) error {
	if s.disk == nil {
		return nil
	}
	// A cond is not a channel: ctx ending wakes the wait by broadcasting.
	stop := context.AfterFunc(ctx, func() {
		s.mu.Lock()
		s.changed.Broadcast()
		s.mu.Unlock()
	})
	defer stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	for (len(s.queue) > 0 || s.writing) && !s.stopped {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.changed.Wait()
	}
	return nil
}

// Close waits for the running refreshes and for the disk writes they and
// everything before them queued, then stops the writer. A ctx that ends first
// ends the wait, and what is not written by then is not written. No refresh
// starts once Close is called, and later changes stay in memory. It is
// idempotent, and safe on a store kept in memory only.
func (s *Store) Close(ctx context.Context) error {
	s.closeOnce.Do(func() { s.closeErr = s.close(ctx) })
	return s.closeErr
}

func (s *Store) close(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()

	// Wait cannot be told to stop, so it runs on its own goroutine, which
	// outlives an abandoned wait only until the refreshes end: each is bounded
	// by refreshTimeout.
	refreshed := make(chan struct{})
	go func() {
		s.refreshes.Wait()
		close(refreshed)
	}()
	var err error
	select {
	case <-refreshed:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if s.disk == nil {
		return err
	}
	if err == nil {
		err = s.drainUntil(ctx)
	}
	s.mu.Lock()
	s.stopped, s.queue = true, nil
	s.changed.Broadcast()
	s.mu.Unlock()
	if err == nil {
		<-s.writerDone
	}
	return err
}

// flush makes ops' writes in order. It takes diskMu, and mu only briefly
// inside it: mu is never held while diskMu is taken.
func (s *Store) flush(ops []diskOp) {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	for _, op := range ops {
		s.mu.Lock()
		current := s.gens[op.key] == op.gen && !s.stopped
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
			if typ := keyType(op.key); !s.unencodable[typ] {
				s.unencodable[typ] = true
				s.logger.Warn("collage: data cache value kept in memory only", "key", keyName(op.key), "err", err)
			}
			return s.disk.remove(op.key)
		}
		payload = encoded
	}
	return s.disk.write(op.header, payload)
}

// encode runs value's Encode, turning a panic into an error: it runs on the
// writer's goroutine, where a panic would crash the process, or, recovered
// higher up, stop every write after it.
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
// rest removed. Their values are decoded on first load, and are stale: served
// at once, and refreshed in the background.
func Open(dir string, opts Options) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("collage: data cache directory: %w", err)
	}
	s := New(opts)
	s.disk = &disk{dir: dir}
	s.gens = make(map[string]uint64)
	s.unencodable = make(map[string]bool)
	s.changed = sync.NewCond(&s.mu)
	s.writerDone = make(chan struct{})
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
		// Every restored value is stale: whatever changed upstream while no
		// process was here to be told is unknown. It is still served at once,
		// and its first load refreshes it.
		staleSince := rec.header.StaleSince
		if !rec.header.Stale {
			staleSince = rec.header.Written
		}
		if staleSince.IsZero() {
			staleSince = s.now()
		}
		stored := &entry{
			key: rec.header.Key, raw: raw, expires: rec.header.Expires, tags: rec.header.Tags,
			stale: true, staleSince: staleSince,
		}
		s.entries[stored.key] = s.order.PushBack(stored)
		s.indexLocked(stored)
	}
	go s.write()
	return s, nil
}
