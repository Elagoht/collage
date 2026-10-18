package datacache

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/types"
)

// Backend is the data half of a cache.SharedCache: where a store a fleet of
// instances shares keeps its values, as bytes. Values are what CacheValue.Encode
// wrote, so the backend never knows their types.
//
// The store keeps no copy of a value in front of it: each Load reads the
// backend, so an invalidation any instance makes reaches every instance's next
// load. What stays in process is what is about this process's own work — the
// fetch running for each key, and the cooldown of a key whose refresh failed.
//
// The overlap rules a store in memory keeps with its own epoch are kept by the
// backend's instead, so that they hold across instances: a fetch reads Epoch
// before it starts, and SetData stores it stale if a tag it carries was
// invalidated since, on any instance, and not at all if one was purged.
type Backend interface {
	Epoch(ctx context.Context) (uint64, error)
	GetData(ctx context.Context, key string) (value []byte, stale, found bool, err error)
	SetData(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string, since uint64) (cache.WriteOutcome, error)
	DeleteData(ctx context.Context, key string) error
}

var _ Backend = cache.SharedCache(nil)

// BackendTimeout bounds each call a load makes to the backend on a request's
// behalf. A cache is there to make a page faster: one slower than this is
// treated as down for that call — a miss, or a write not made — rather than
// let a stalled connection hold every page that reads data. A background
// refresh, which nobody waits on, keeps its own, longer budget.
const BackendTimeout = 500 * time.Millisecond

// backendQuiet is how long a backend that keeps failing is not reported again;
// see cache.SharedHealth.
const backendQuiet = cache.SharedQuiet

// cooldown is a key whose refreshes failed, failures times in a row: no refresh
// starts for it before retryAt. tags are the ones it was loaded with, so an
// invalidation of one of them ends the cooldown as it does for a stored entry.
// seq orders the cooldowns by when they began, for eviction.
type cooldown struct {
	failures int
	retryAt  time.Time
	tags     []string
	seq      uint64
}

// backendDone records how a call to the backend made for parent went, in the
// store's SharedHealth: the App's, shared with the page paths, so a backend
// failing under both is reported once per window, not once per path or per
// load. A failure while parent is done is the caller leaving, not news about
// the backend, and is not counted either way.
func (s *Store) backendDone(parent context.Context, op, key string, err error) {
	s.health.Done(parent, "data "+op, keyName(key), err)
}

// loadShared is load for a store with a backend.
func (s *Store) loadShared(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (types.CacheValue, error), decode func([]byte) (types.CacheValue, error)) (value types.CacheValue, stale bool, err error, again bool) {
	if stored, stale, found := s.readShared(ctx, key, decode); found {
		if !stale {
			return stored, false, nil, false
		}
		if !types.WantsFreshData(ctx) {
			s.mu.Lock()
			if cooling, ok := s.cooling[key]; !ok || !s.now().Before(cooling.retryAt) {
				s.refreshSharedLocked(ctx, key, ttl, tags, fetch)
			}
			s.mu.Unlock()
			s.metrics.CacheEvent(ctx, observability.CacheStale, keyName(key))
			return stored, true, nil, false
		}
		// Fresh data wanted: wait below like a miss. The stale value stays in
		// the backend until a successful fetch replaces it.
	}

	s.mu.Lock()
	if running, ok := s.inflight[key]; ok {
		return s.awaitLocked(ctx, running)
	}
	started := &call{done: make(chan struct{}), epoch: s.epoch}
	s.inflight[key] = started
	s.mu.Unlock()

	// As in load: a fetch that panics still gives the key up and answers its
	// waiters.
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
	// Read before the fetch, so an invalidation that comes while it runs, on
	// any instance, is after since.
	epochCtx, cancel := context.WithTimeout(ctx, s.backendTimeout)
	since, sinceErr := s.backend.Epoch(epochCtx)
	cancel()
	s.backendDone(ctx, "epoch", key, sinceErr)
	value, err = fetch(ctx)
	finished = true
	started.value, started.err = value, err
	started.abandoned = err != nil && ctx.Err() != nil

	// Written before the key is given up, so a load that comes after the
	// waiters are answered finds the value rather than fetching it again. The
	// value is good whether or not the reader is still there to take it, so a
	// reader who left once it was fetched does not cost the write.
	detached := context.WithoutCancel(ctx)
	writeCtx, cancel := context.WithTimeout(detached, s.backendTimeout)
	started.overtaken = s.settleShared(detached, writeCtx, key, value, err, ttl, tags, since, sinceErr)
	cancel()
	s.mu.Lock()
	delete(s.inflight, key)
	// A fresh-data load replacing a value whose refreshes failed is the
	// recovery as much as a refresh would have been.
	failures := 0
	if cooling, ok := s.cooling[key]; ok && (err == nil || errors.Is(err, types.ErrNotFound)) {
		if err == nil {
			failures = cooling.failures
		}
		delete(s.cooling, key)
	}
	s.mu.Unlock()
	close(started.done)
	if failures > 0 {
		s.logger.Info("collage: data refreshed after failures", "key", keyName(key), "failures", failures)
	}
	return value, false, err, false
}

// readShared returns the value the backend holds under key, decoded. A backend
// that fails is a miss, and a value that no longer decodes is deleted and is
// one as well.
func (s *Store) readShared(ctx context.Context, key string, decode func([]byte) (types.CacheValue, error)) (value types.CacheValue, stale, found bool) {
	readCtx, cancel := context.WithTimeout(ctx, s.backendTimeout)
	raw, stale, found, err := s.backend.GetData(readCtx, key)
	cancel()
	s.backendDone(ctx, "read", key, err)
	if err != nil || !found {
		return nil, false, false
	}
	decoded, err := guardedDecode(decode, raw)
	if err != nil {
		// The type changed between builds in a way its identity did not
		// catch, or another build wrote it. A miss, never the page's error.
		s.logger.Warn("collage: dropping a stored value that no longer decodes", "key", keyName(key), "err", err)
		deleteCtx, cancel := context.WithTimeout(ctx, s.backendTimeout)
		s.backendDone(ctx, "delete", key, s.backend.DeleteData(deleteCtx, key))
		cancel()
		return nil, false, false
	}
	return decoded, stale, true
}

// refreshSharedLocked starts the background fetch replacing key's stale value,
// unless one is already running. It must be called with s.mu held.
func (s *Store) refreshSharedLocked(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (types.CacheValue, error)) {
	if _, running := s.inflight[key]; running || s.closing {
		return
	}
	started := &call{done: make(chan struct{}), epoch: s.epoch}
	s.inflight[key] = started
	s.refreshes.Add(1)
	go s.refreshShared(context.WithoutCancel(ctx), key, ttl, tags, fetch, started)
}

func (s *Store) refreshShared(detached context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (types.CacheValue, error), started *call) {
	defer s.refreshes.Done()
	// detached never ends, so a backend call that runs out of the refresh's
	// budget counts as the backend's failure.
	ctx, cancel := context.WithTimeout(detached, s.refreshTimeout)
	defer cancel()
	since, sinceErr := s.backend.Epoch(ctx)
	s.backendDone(detached, "epoch", key, sinceErr)
	value, err := guarded(ctx, fetch)
	started.value, started.err = value, err
	started.overtaken = s.settleShared(detached, ctx, key, value, err, ttl, tags, since, sinceErr)

	s.mu.Lock()
	delete(s.inflight, key)
	cooling := s.cooling[key]
	failures := 0
	switch {
	case err == nil || errors.Is(err, types.ErrNotFound):
		if cooling != nil {
			failures = cooling.failures
		}
		delete(s.cooling, key)
	default:
		// Unlike in memory, the value is the backend's, and whether it is
		// still there is not known here: the key cools down either way, and a
		// load that finds nothing fetches and meets the error itself.
		if cooling == nil {
			s.makeRoomForCooldownLocked()
			s.coolSeq++
			cooling = &cooldown{seq: s.coolSeq}
			s.cooling[key] = cooling
		}
		cooling.tags = slices.Clone(tags)
		cooling.failures++
		cooling.retryAt = s.now().Add(retryBackoff(cooling.failures))
		failures = cooling.failures
	}
	s.mu.Unlock()
	close(started.done)

	switch {
	case err == nil:
		if failures > 0 {
			s.logger.Info("collage: data refreshed after failures", "key", keyName(key), "failures", failures)
		}
	case errors.Is(err, types.ErrNotFound):
	default:
		s.logger.Warn("collage: serving stale data, its refresh failed",
			"key", keyName(key), "failures", failures, "err", err)
	}
}

// makeRoomForCooldownLocked keeps the cooldowns within the store's bound, as
// entries are in memory: keys can come from requests, and a failing upstream
// would otherwise grow the map with every key it fails. A cooldown already
// over goes first, as it no longer holds anything back; else the oldest. It
// must be called with s.mu held.
func (s *Store) makeRoomForCooldownLocked() {
	if s.max <= 0 || len(s.cooling) < s.max {
		return
	}
	now := s.now()
	var oldest string
	var oldestSeq uint64
	for key, cooling := range s.cooling {
		if !now.Before(cooling.retryAt) {
			delete(s.cooling, key)
			return
		}
		if oldest == "" || cooling.seq < oldestSeq {
			oldest, oldestSeq = key, cooling.seq
		}
	}
	delete(s.cooling, oldest)
}

// settleShared records in the backend what a fetch brought back: a value is
// written, which the backend stores stale or not at all if an invalidation or a
// purge came after since; not found deletes what was there; any other error
// leaves it. Without since there is no telling an invalidation that overlapped
// the fetch, so nothing is written. A backend that fails costs the write, not
// the value, which the caller still returns. overtaken reports a write the
// backend stored stale or skipped. ctx bounds the backend calls; parent is
// whose leaving makes a failed one no news about the backend.
func (s *Store) settleShared(parent, ctx context.Context, key string, value types.CacheValue, err error, ttl time.Duration, tags []string, since uint64, sinceErr error) (overtaken bool) {
	switch {
	case err == nil:
		if sinceErr != nil {
			return false
		}
		encoded, encodeErr := encode(value)
		if encodeErr != nil {
			// Kept nowhere: a copy in this process alone would be the
			// per-instance data a shared backend is there to end.
			s.diskMu.Lock()
			if typ := keyType(key); !s.unencodable[typ] {
				s.unencodable[typ] = true
				s.logger.Warn("collage: data cache value not shared, it cannot be encoded", "key", keyName(key), "err", encodeErr)
			}
			s.diskMu.Unlock()
			return false
		}
		outcome, setErr := s.backend.SetData(ctx, key, encoded, max(ttl, 0), tags, since)
		s.backendDone(parent, "write", key, setErr)
		return setErr == nil && outcome != cache.WriteFresh
	case errors.Is(err, types.ErrNotFound):
		s.backendDone(parent, "delete", key, s.backend.DeleteData(ctx, key))
	}
	return false
}

// endCooldownsLocked ends the cooldown of every key under any of tags: an
// invalidation is news that the upstream is answering, so the key tries on its
// next load, its failures kept so one more backs off from there. A purge
// forgets the key's failures too, as it removes a stored entry. It must be
// called with s.mu held. Only a store with a backend keeps cooldowns here; for
// one without, cooling is empty and this does nothing.
func (s *Store) endCooldownsLocked(tags []string, purge bool) {
	for key, cooling := range s.cooling {
		if !slices.ContainsFunc(cooling.tags, func(tag string) bool { return slices.Contains(tags, tag) }) {
			continue
		}
		if purge {
			delete(s.cooling, key)
		} else {
			cooling.retryAt = time.Time{}
		}
	}
}
