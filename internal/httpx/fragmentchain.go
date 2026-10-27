package httpx

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/Elagoht/collage/internal/render"
)

// ServeFragment runs r — a GET for a fragment path, built by the caller — through
// the middleware chain a request to r's URL would pass, and calls serve with the
// writer and request the chain's innermost handler receives. It is how
// Host.RenderFragment renders a fragment as a request to the fragment's URL would
// be served, for a plugin that pushes renders over a stream of its own: a
// middleware guarding a path — a basicauth, an application's own login check, a
// rate limit — sees the fragment's path and decides about it, not about the
// stream's path, which it already let through once.
//
// It reports whether serve ran to its end while the chain was still running —
// the one outcome in which what serve produced is the request's answer. When it
// did not, status is what a middleware answered with (200 when it wrote
// nothing): it answered instead of passing the request on, or answered over a
// render it had passed on — a recovering middleware over a panic, a
// http.TimeoutHandler over a slow render. Nothing a middleware writes goes
// anywhere — its body, its headers and its cookies are written into a writer
// that keeps only the status — since the caller has a response of its own, or
// none.
//
// It does not return while serve may still run: a render the chain left
// running — a TimeoutHandler returns before the handler it timed — is waited
// for, and a middleware calling next once the chain has returned, from a
// goroutine of its own, gets no render. So what serve writes is the caller's to
// read once ServeFragment has returned, and never after.
//
// What ServeHTTP gives a request before the chain, the request gets here too: the
// trusted proxies, a route record, the origin resolver and a fresh set of cache
// declarations, so a middleware calling Vary or SkipCache does so as over HTTP.
// The request hooks, the span, the metrics and the path checks are not run: this
// is no request from the wire, and the caller has checked its path.
//
// A panic, in a middleware or in serve, is returned as an error wrapping ErrPanic,
// a panic in serve even when a middleware recovered it and answered: a plugin
// renders from goroutines of its own, where a panic would end the process.
func (h *Handler) ServeFragment(r *http.Request, serve func(http.ResponseWriter, *http.Request)) (served bool, status int, err error) {
	ctx := r.Context()
	if len(h.trusted) > 0 {
		ctx = WithTrustedProxies(ctx, h.trusted)
	}
	ctx = context.WithValue(ctx, routeCtxKey{}, &routeRef{start: time.Now(), routes: &h.routes})
	if h.origins != nil {
		ctx = WithOrigins(ctx, h.origins)
	}
	// Fresh, not the caller's: the request a plugin renders for is usually one
	// that went through the chain itself — the stream's — and its declarations
	// were frozen when it was routed. Those were about the stream's response.
	ctx = context.WithValue(ctx, varySetKey{}, &varySet{})

	run := &fragmentRun{serve: serve, finished: make(chan struct{})}
	w := &fragmentWriter{header: make(http.Header), run: run}
	r = r.WithContext(context.WithValue(ctx, fragmentRunKey{}, run))
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("%w: %w", ErrPanic, &render.PanicError{Value: recovered, Stack: debug.Stack()})
			}
		}()
		if h.chain == nil {
			run.arrive(w, r)
			return
		}
		h.chain.ServeHTTP(w, r)
	}()
	return run.close(w, err)
}

// fragmentRun is one ServeFragment's way through the chain. The chain is composed
// once, for every request, so the render it ends in cannot be closed over; it
// travels in the request context, as chainState does, and on the writer, for a
// middleware that replaced the context rather than derived from it.
//
// Its state is guarded by mu: a middleware may call next from a goroutine of its
// own, and write to the writer from there.
type fragmentRun struct {
	serve func(http.ResponseWriter, *http.Request)
	// finished is closed when serve has returned, or panicked.
	finished chan struct{}

	mu sync.Mutex
	// reached is set when serve is called; closed when the chain has returned,
	// after which serve is not called any more.
	reached, closed bool
	// served is set when serve returned normally before closed was.
	served bool
	// panicked is what serve panicked with, if it did.
	panicked *render.PanicError
	// status is the first status the middleware wrote.
	status int
}

type fragmentRunKey struct{}

// arrive runs the render for the request the chain handed its innermost
// handler. Once only: a middleware calling next twice gets one render, the
// first. Not at all once the chain has returned.
func (run *fragmentRun) arrive(w http.ResponseWriter, r *http.Request) {
	run.mu.Lock()
	if run.reached || run.closed {
		run.mu.Unlock()
		return
	}
	run.reached = true
	run.mu.Unlock()

	normal := false
	defer func() {
		run.mu.Lock()
		if normal && !run.closed {
			run.served = true
		}
		run.mu.Unlock()
		close(run.finished)
	}()
	defer func() {
		if normal {
			return
		}
		// Recorded, then passed on: over HTTP a panic below the chain unwinds
		// through the middleware, and a recovering one answers it. Recorded so
		// that its answer is not taken for the render's.
		if recovered := recover(); recovered != nil {
			run.mu.Lock()
			run.panicked = &render.PanicError{Value: recovered, Stack: debug.Stack()}
			run.mu.Unlock()
			panic(recovered)
		}
	}()
	// Middleware is done, as serve settles it for a request from the wire.
	freezeDeclarations(r)
	// The render's request does not carry the run: a render that sends a
	// request of its own through the handler — a capture — is served, not
	// taken for this run arriving again.
	r = r.WithContext(context.WithValue(r.Context(), fragmentRunKey{}, (*fragmentRun)(nil)))
	run.serve(w, r)
	normal = true
}

// close ends the run once the chain has returned, waiting for a render it left
// running, and reports ServeFragment's results: whether serve ran to its end
// within the chain, the status a middleware answered with otherwise, and the
// error, chainErr — a panic that escaped the chain — or the panic a middleware
// recovered from serve.
func (run *fragmentRun) close(w *fragmentWriter, chainErr error) (served bool, status int, err error) {
	run.mu.Lock()
	run.closed = true
	reached := run.reached
	run.mu.Unlock()
	if reached {
		// Not returned before it has: what it writes is read once this returns.
		<-run.finished
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if chainErr != nil {
		return false, 0, chainErr
	}
	if run.panicked != nil {
		return false, 0, fmt.Errorf("%w: %w", ErrPanic, run.panicked)
	}
	if run.served {
		return true, 0, nil
	}
	if run.status == 0 {
		return false, http.StatusOK, nil
	}
	return false, run.status, nil
}

// fragmentRunOf returns the ServeFragment run that r, arriving at the end of the
// chain through w, belongs to, or nil for a request from the wire.
func fragmentRunOf(w http.ResponseWriter, r *http.Request) *fragmentRun {
	if run, ok := r.Context().Value(fragmentRunKey{}).(*fragmentRun); ok && run != nil {
		return run
	}
	for w != nil {
		if fw, ok := w.(*fragmentWriter); ok {
			return fw.run
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w = unwrapper.Unwrap()
	}
	return nil
}

// fragmentWriter is the response a fragment render's middleware writes to:
// it keeps the status and throws the rest away.
type fragmentWriter struct {
	header http.Header
	run    *fragmentRun
}

func (w *fragmentWriter) Header() http.Header { return w.header }

// WriteHeader keeps the first status, under the run's lock: a middleware may
// write from a goroutine of its own.
func (w *fragmentWriter) WriteHeader(status int) {
	w.run.mu.Lock()
	if w.run.status == 0 {
		w.run.status = status
	}
	w.run.mu.Unlock()
}

func (w *fragmentWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return len(p), nil
}

// Flush does nothing, so a middleware that flushes — a compressor, an event
// stream's — finds a Flusher, as over HTTP.
func (w *fragmentWriter) Flush() {}

// IsCleanPath reports whether p is a path ServeHTTP serves as it is, rather than
// redirecting it to its clean spelling.
func IsCleanPath(p string) bool {
	_, dirty := cleanPath(p)
	return !dirty
}
