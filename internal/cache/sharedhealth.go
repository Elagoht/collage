package cache

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// SharedQuiet is how long a SharedCache that keeps failing is not reported
// again: down, it fails on every page and every load, and one line for each
// would drown the log.
const SharedQuiet = 30 * time.Second

// SharedHealth is whether a SharedCache is answering, for the log. One is kept
// per App and handed to both of the store's users — the page and document
// paths and the data cache — so a store failing under both reports into one
// window: one warning, not one per path.
//
// A failure warns when no warning was logged in the last SharedQuiet, and is
// only counted otherwise; the first success after a warned outage says the
// store is back, once. The window outlives a recovery: a store that fails and
// answers by turns — a full Redis under noeviction answers the epoch and every
// read and fails each write with OOM; a restarting one fails one command per
// dead pooled connection — still gets one warning and one recovery per window,
// not one of each per request.
//
// A SharedCache's failure is a miss or a write not made, never the request's
// failure, so it is logged here and nowhere else: not as a request's error, and
// not as a plugin's ErrorEvent, which would carry it once per cacheable miss.
//
// It is safe for concurrent use. A success while the store is up takes no lock.
type SharedHealth struct {
	logger *slog.Logger
	// now is the clock the window is kept by; a test replaces it before use.
	now  func() time.Time
	down atomic.Bool
	mu   sync.Mutex
	// warned is whether this outage has logged a warning, and so whether its
	// end is logged.
	warned   bool
	warnedAt time.Time
	// suppressed counts the failures not logged since the last warning,
	// across outages that ended without one.
	suppressed int
}

// NewSharedHealth returns a SharedHealth logging to logger, keeping its window
// by now. A nil logger is slog.Default(); a nil now is time.Now.
func NewSharedHealth(logger *slog.Logger, now func() time.Time) *SharedHealth {
	if logger == nil {
		logger = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &SharedHealth{logger: logger, now: now}
}

// Done records how one call to the store made for parent went. op names the
// call, and key the entry it was about ("" for none), in the log. A failure
// while parent is done is the caller leaving, not news about the store, and is
// counted neither way.
func (h *SharedHealth) Done(parent context.Context, op, key string, err error) {
	if err == nil {
		if h.down.Load() {
			h.recovered()
		}
		return
	}
	if parent.Err() != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.down.Store(true)
	now := h.now()
	if !h.warnedAt.IsZero() && now.Sub(h.warnedAt) < SharedQuiet {
		h.suppressed++
		return
	}
	msg := "collage: shared page and data cache unavailable, reads miss and writes are skipped"
	if h.warned {
		msg = "collage: shared page and data cache still unavailable"
	}
	h.logger.Warn(msg, "op", op, "key", key, "err", err, "suppressed", h.suppressed)
	h.warned, h.warnedAt, h.suppressed = true, now, 0
}

// recovered ends an outage. One that logged a warning logs, once, that the
// store answers again, with the failures not reported since; one that did not
// ends silently, its failures left counted for the next warning.
func (h *SharedHealth) recovered() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.down.Load() {
		// Another call saw the recovery first.
		return
	}
	h.down.Store(false)
	if !h.warned {
		return
	}
	h.logger.Info("collage: shared page and data cache available again", "suppressed", h.suppressed)
	h.warned, h.suppressed = false, 0
}
