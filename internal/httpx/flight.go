package httpx

import (
	"context"
	"errors"
	"sync"
	"time"
)

// outcome is one render's product: what to write, or why nothing could be.
//
// It exists so that a render can be performed on behalf of more than one request.
// Writing to a ResponseWriter inside the render would tie the work to the one
// request that happened to start it; producing a value instead lets every request
// waiting on the same page write its own response from the same bytes.
type outcome struct {
	// content is the HTML to write, already passed through AfterRender.
	content []byte
	// etag is what the response advertises — the cache's, when the cache wrote
	// the entry, and the framework's own otherwise.
	etag string
	// renderTime is how long the render took, reported in dev mode.
	renderTime time.Duration
	// fail is non-nil when no content could be produced, and carries everything
	// serveFailure needs.
	fail *failure
}

// flight coalesces concurrent renders of the same cache key.
//
// The problem it solves is the moment a popular page's cache entry expires. Without
// it, every request that arrives in the window between the expiry and the first
// completed re-render is a cache miss, and every one of them renders: a hundred
// identical renders, each making the same upstream calls, at the exact moment the
// upstream is least able to absorb them. The work is not merely wasted — it is
// wasted in a way that grows with traffic, which is the shape of an outage rather
// than of a slow page.
//
// So the first request for a key renders and the rest wait for it. What they get is
// the same *outcome, which each of them writes as its own response.
//
// The key is the cache key rather than the path: it already encodes locale, path
// parameters and the query dimensions the page declared, so two requests share a
// render exactly when they would have shared a cache entry. Anything coarser would
// hand one request another's page.
type flight struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

// flightCall is one in-progress render and the requests waiting on it.
type flightCall struct {
	done chan struct{}
	out  *outcome
	// waiters counts the requests that joined this render rather than starting
	// one. It is guarded by the flight's mutex, and it is what lets a test know
	// that a waiter has actually joined — the one fact the tests cannot observe
	// from the outside, and the one they would otherwise have to guess at with a
	// sleep.
	waiters int
}

func newFlight() *flight {
	return &flight{calls: make(map[string]*flightCall)}
}

// do returns the outcome for key, running fn to produce it if no one else already
// is. The bool reports whether the outcome came from another request's render,
// which is what distinguishes a coalesced request from one that did the work.
//
// fn runs under the leader's context. That is worth stating plainly, because it is
// the one way this can go wrong: a leader whose request is cancelled produces a
// cancellation failure, and handing that to everyone waiting would turn one reader
// pressing stop into an error page for every reader who asked at the same moment.
// So a waiter that receives a leader's cancellation failure, while its own request
// is still live, tries again — becoming the leader itself if no one else has.
func (f *flight) do(ctx context.Context, key string, fn func() *outcome) (*outcome, bool) {
	for {
		f.mu.Lock()
		if existing, running := f.calls[key]; running {
			existing.waiters++
			f.mu.Unlock()

			select {
			case <-existing.done:
			case <-ctx.Done():
				// This request is over. Waiting out a leader that may have a
				// longer deadline would hold the goroutine for a response
				// nobody will read.
				return &outcome{fail: &failure{err: ctx.Err(), stage: stageRender}}, true
			}

			if !leaderWasCancelled(existing.out) {
				return existing.out, true
			}
			// The leader's failure was its own request ending, which says
			// nothing about whether this one can be served. Round again.
			if err := ctx.Err(); err != nil {
				return &outcome{fail: &failure{err: err, stage: stageRender}}, true
			}
			continue
		}

		call := &flightCall{done: make(chan struct{})}
		f.calls[key] = call
		f.mu.Unlock()

		// The entry is removed before the waiters are woken, so a waiter that
		// arrives in between starts a fresh render rather than joining one that
		// has already finished.
		call.out = fn()
		f.mu.Lock()
		delete(f.calls, key)
		f.mu.Unlock()
		close(call.done)

		return call.out, false
	}
}

// inFlight reports how many renders are running. It exists for the tests, which
// have no other way to see that a finished flight left nothing behind.
func (f *flight) inFlight() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// waitingOn reports how many requests have joined the render of key. Like
// inFlight, it exists for the tests.
func (f *flight) waitingOn(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	call, running := f.calls[key]
	if !running {
		return 0
	}
	return call.waiters
}

// leaderWasCancelled reports whether an outcome failed because the request that
// produced it ended, rather than because the page could not be rendered.
func leaderWasCancelled(out *outcome) bool {
	if out == nil || out.fail == nil {
		return false
	}
	return errors.Is(out.fail.err, context.Canceled) ||
		errors.Is(out.fail.err, context.DeadlineExceeded)
}
