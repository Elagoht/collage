package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// HandlerMount is an http.Handler of the application's own, answering every
// request under Prefix: an API written with any router, a gRPC gateway, a
// webhook receiver.
//
// Collage does not look inside it. No forgery check, no body limit, no cache —
// whoever mounts it owns what it does. What it gets from the framework is what
// every request gets: the span, the metrics, the panic guard, the middleware,
// and the shutdown drain.
type HandlerMount struct {
	// Prefix begins with "/". Ending in "/", it claims every path beneath it;
	// otherwise it is one exact path, "/metrics". The request is passed on
	// unchanged, so the handler sees the full path; wrap it in http.StripPrefix
	// if it expects otherwise.
	Prefix string
	// Handler answers every request whose path begins with Prefix.
	Handler http.Handler
}

// Matches reports whether the mount answers path.
func (m HandlerMount) Matches(path string) bool {
	if strings.HasSuffix(m.Prefix, "/") {
		return strings.HasPrefix(path, m.Prefix)
	}
	return path == m.Prefix
}

// Overlaps reports whether the mount and one claiming other would both answer
// some path.
func (m HandlerMount) Overlaps(other string) bool {
	return m.Matches(other) || HandlerMount{Prefix: other}.Matches(m.Prefix)
}

// ErrHandlerFailed is reported to the error hooks when a mounted handler answers
// with a server error. Its 4xx answers are its own business and are not reported:
// a 404 from an API is an answer, not a failure of the site.
var ErrHandlerFailed = errors.New("collage: mounted handler failed")

// serveHandler runs a mounted handler and returns the status it wrote.
func (h *Handler) serveHandler(w http.ResponseWriter, r *http.Request, mount HandlerMount, route *routeRef) int {
	capture := &statusCapturingWriter{ResponseWriter: w}
	mount.Handler.ServeHTTP(capture, r)

	status := capture.Status()
	if status >= http.StatusInternalServerError {
		h.reportError(r, route.failure(status, stageHandler, fmt.Errorf("%w: status %d", ErrHandlerFailed, status)))
	}
	return status
}

// Middleware wraps the framework's handling of a request, in the standard
// net/http shape.
type Middleware = func(http.Handler) http.Handler

// chainState carries what serveGuarded needs back from the far end of the
// middleware chain: the route the request resolved to, and the status serve
// wrote. It travels in the request context, because a middleware passes on a
// request of its own making and the framework has no other way to reach it.
type chainState struct {
	route  *routeRef
	status int
}

type chainStateKey struct{}

// compose wraps final in middleware, the first element outermost: the order they
// were registered in is the order a request passes through them.
func compose(middleware []Middleware, final http.Handler) http.Handler {
	handler := final
	for i := len(middleware) - 1; i >= 0; i-- {
		handler = middleware[i](handler)
	}
	return handler
}

// serveChained is the innermost handler of the middleware chain.
func (h *Handler) serveChained(w http.ResponseWriter, r *http.Request) {
	state, ok := r.Context().Value(chainStateKey{}).(*chainState)
	if !ok {
		// A middleware replaced the request context rather than deriving from
		// it. The request is still served; the status is then read from what
		// was written, and a panic below is answered as a page failure.
		h.serve(w, r, &routeRef{})
		return
	}
	state.status = h.serve(w, r, state.route)
}

// ErrVaryOutsideRequest is returned by Vary for a request that is not being
// served by a collage handler.
var ErrVaryOutsideRequest = errors.New("collage: Vary or SkipCache called on a request collage is not serving")

// ErrVaryTooLate is returned by Vary once the request's cache key has been
// computed. A dimension declared after the lookup would be one the lookup
// ignored, so the framework says so rather than caching a page under a key that
// does not describe it.
var ErrVaryTooLate = errors.New("collage: Vary or SkipCache called after routing; call it from middleware")

// varySet is one request's declared cache dimensions.
//
// It lives in the request context from the moment the framework receives the
// request, so a middleware can add to it without having to hand a new request
// on, and it is frozen when middleware is done and routing begins.
type varySet struct {
	mu      sync.Mutex
	values  map[string]string
	headers []string
	frozen  bool
	// skip is set by SkipCache: this request is neither answered from the cache
	// nor stored in it.
	skip bool
}

type varySetKey struct{}

// withVarySet returns r carrying a fresh, empty varySet.
func withVarySet(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), varySetKey{}, &varySet{}))
}

// Vary declares that the response to r depends on the request header named
// header, and that the value it resolved to is value.
//
// The value, not the raw header, is what enters the cache key — so a middleware
// that reduces Accept-Language to "tr" or "en" keeps two cache entries rather
// than one per spelling of a browser's preferences. The header name goes into
// the response's Vary header, so a shared cache between the server and the
// reader keeps them apart too. Declaring the same header twice keeps the last
// value.
//
// It must be called before routing, which in practice means
// from middleware registered with App.Use.
func Vary(r *http.Request, header, value string) error {
	set, ok := r.Context().Value(varySetKey{}).(*varySet)
	if !ok {
		return ErrVaryOutsideRequest
	}
	header = http.CanonicalHeaderKey(strings.TrimSpace(header))
	if header == "" {
		return fmt.Errorf("collage: Vary needs a header name")
	}

	set.mu.Lock()
	defer set.mu.Unlock()
	if set.frozen {
		return ErrVaryTooLate
	}
	if set.values == nil {
		set.values = make(map[string]string)
	}
	if _, seen := set.values[header]; !seen {
		set.headers = append(set.headers, header)
	}
	set.values[header] = value
	return nil
}

// SkipCache declares that r is answered with a fresh render that is neither read
// from the page cache nor written to it, and marked private and no-store.
//
// It is what a preview needs: an editor looking at an unpublished draft must not be
// served the published page from the cache, and the draft must not become the page
// every other reader is served. Who may skip the cache is the application's to
// decide, in its own middleware — a signed cookie, a session, a secret in the URL.
//
// Like Vary it must be called before routing, which in practice
// means from middleware registered with App.Use.
func SkipCache(r *http.Request) error {
	set, ok := r.Context().Value(varySetKey{}).(*varySet)
	if !ok {
		return ErrVaryOutsideRequest
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	if set.frozen {
		return ErrVaryTooLate
	}
	set.skip = true
	return nil
}

// freezeDeclarations closes r's Vary and SkipCache declarations: from here on,
// either returns ErrVaryTooLate.
func freezeDeclarations(r *http.Request) {
	if set, ok := r.Context().Value(varySetKey{}).(*varySet); ok {
		set.mu.Lock()
		set.frozen = true
		set.mu.Unlock()
	}
}

// requestSkipsCache reports whether r declared SkipCache, and freezes its
// declarations: whether the cache is consulted has now been decided.
func requestSkipsCache(r *http.Request) bool {
	set, ok := r.Context().Value(varySetKey{}).(*varySet)
	if !ok {
		return false
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	set.frozen = true
	return set.skip
}

// skipsCache reports whether r declared SkipCache, without freezing anything.
func skipsCache(r *http.Request) bool {
	set, ok := r.Context().Value(varySetKey{}).(*varySet)
	if !ok {
		return false
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	return set.skip
}

// requestVary freezes r's declared dimensions and returns them as cache-key
// entries, sorted. A request with none returns nil.
func requestVary(r *http.Request) []string {
	set, ok := r.Context().Value(varySetKey{}).(*varySet)
	if !ok {
		return nil
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	set.frozen = true
	if len(set.values) == 0 {
		return nil
	}
	entries := make([]string, 0, len(set.values))
	for header, value := range set.values {
		entries = append(entries, header+"="+value)
	}
	sort.Strings(entries)
	return entries
}

// requestVaryHeaders returns the header names r declared, in declaration order.
func requestVaryHeaders(r *http.Request) []string {
	set, ok := r.Context().Value(varySetKey{}).(*varySet)
	if !ok {
		return nil
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	return append([]string(nil), set.headers...)
}
