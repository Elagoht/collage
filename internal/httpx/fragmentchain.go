package httpx

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
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
// It reports whether serve ran. When it did not, a middleware answered the
// request itself, and status is what it answered with: 200 when it wrote
// nothing. Nothing a middleware writes goes anywhere — its body, its headers
// and its cookies are written into a writer that keeps only the status — since
// the caller has a response of its own, or none.
//
// What ServeHTTP gives a request before the chain, the request gets here too: the
// trusted proxies, a route record, the origin resolver and a fresh set of cache
// declarations, so a middleware calling Vary or SkipCache does so as over HTTP.
// The request hooks, the span, the metrics and the path checks are not run: this
// is no request from the wire, and the caller has checked its path.
//
// A panic, in a middleware or in serve, is returned as an error wrapping ErrPanic:
// a plugin renders from goroutines of its own, where a panic would end the
// process.
func (h *Handler) ServeFragment(r *http.Request, serve func(http.ResponseWriter, *http.Request)) (reached bool, status int, err error) {
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

	run := &fragmentRun{serve: serve}
	w := &fragmentWriter{header: make(http.Header), run: run}
	defer func() {
		if recovered := recover(); recovered != nil {
			reached, status = run.reached, 0
			err = fmt.Errorf("%w: %w", ErrPanic, &render.PanicError{Value: recovered, Stack: debug.Stack()})
		}
	}()

	r = r.WithContext(context.WithValue(ctx, fragmentRunKey{}, run))
	if h.chain == nil {
		run.arrive(w, r)
		return true, 0, nil
	}
	h.chain.ServeHTTP(w, r)
	if run.reached {
		return true, 0, nil
	}
	return false, w.statusOr(http.StatusOK), nil
}

// fragmentRun is one ServeFragment's way through the chain. The chain is composed
// once, for every request, so the render it ends in cannot be closed over; it
// travels in the request context, as chainState does, and on the writer, for a
// middleware that replaced the context rather than derived from it.
type fragmentRun struct {
	serve   func(http.ResponseWriter, *http.Request)
	reached bool
}

type fragmentRunKey struct{}

// arrive runs the render for the request the chain handed its innermost
// handler. Once only: a middleware calling next twice gets one render, the
// first.
func (run *fragmentRun) arrive(w http.ResponseWriter, r *http.Request) {
	if run.reached {
		return
	}
	run.reached = true
	// Middleware is done, as serve settles it for a request from the wire.
	freezeDeclarations(r)
	// The render's request does not carry the run: a render that sends a
	// request of its own through the handler — a capture — is served, not
	// taken for this run arriving again.
	r = r.WithContext(context.WithValue(r.Context(), fragmentRunKey{}, (*fragmentRun)(nil)))
	run.serve(w, r)
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
	status int
	run    *fragmentRun
}

func (w *fragmentWriter) Header() http.Header { return w.header }

func (w *fragmentWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *fragmentWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return len(p), nil
}

// Flush does nothing, so a middleware that flushes — a compressor, an event
// stream's — finds a Flusher, as over HTTP.
func (w *fragmentWriter) Flush() {}

func (w *fragmentWriter) statusOr(fallback int) int {
	if w.status == 0 {
		return fallback
	}
	return w.status
}

// IsCleanPath reports whether p is a path ServeHTTP serves as it is, rather than
// redirecting it to its clean spelling.
func IsCleanPath(p string) bool {
	_, dirty := cleanPath(p)
	return !dirty
}
