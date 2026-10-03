package httpx

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// sharedRequest returns the request a render stored in the cache is made from:
// r, with only what the cache key holds.
//
// One render of a cacheable page is served to every reader whose request has
// its key, so the render must not see what differs between them. It used to be
// handed the first reader's whole request — their cookie, their credentials,
// their address, every query parameter — and a handler that read one wrote it
// into the copy everyone after was given: a session shown to strangers, a
// tracking parameter frozen into a page. The promise a cacheable page makes,
// that its output is the same for everyone, is kept here instead of trusted.
//
// What stays is what the key is made of: the path, the host, the query
// parameters the page names with CacheParams (all of them when it names none,
// since then the whole query is in the key), and the headers middleware declared
// with collage.Vary.
//
// The context's values are stripped the same way, by renderSafeContext at the
// call site: a value an application's middleware stored under its own key — a
// signed-in user, a request id, a tenant — is per-reader too, and a data handler
// that read one during a shared render would write it into the page every later
// reader is served. Only the framework's own request-independent keys (the route,
// the vary set, the external-chain state) are kept, because they are the same for
// every reader of one cached page; a middleware's own value is read back through
// collage.Varied, from the vary set, which is in the key. Cancellation and
// deadline are left to the caller's parent context; this function only rebuilds
// the request.
func sharedRequest(r *http.Request, cacheParams []string) *http.Request {
	shared := r.Clone(r.Context())
	shared.Header = make(http.Header)
	for _, name := range requestVaryHeaders(r) {
		key := http.CanonicalHeaderKey(name)
		if values, ok := r.Header[key]; ok {
			shared.Header[key] = append([]string(nil), values...)
		}
	}
	if cacheParams != nil {
		shared.URL.RawQuery = strings.Join(queryVary(r.URL, cacheParams), "")
		shared.RequestURI = shared.URL.RequestURI()
	}
	shared.Form, shared.PostForm, shared.MultipartForm = nil, nil, nil
	shared.Body, shared.ContentLength, shared.Trailer = http.NoBody, 0, nil
	shared.RemoteAddr = ""
	if r.TLS != nil {
		// Still TLS, so a scheme reads right; not the reader's certificate.
		state := *r.TLS
		state.PeerCertificates, state.VerifiedChains = nil, nil
		shared.TLS = &state
	}
	return shared
}

// renderSafeContext wraps parent so a shared, cacheable render sees only the
// context values that are the same for every reader of the page it produces:
// the framework's own route, vary, external-chain state and origin resolver. A value an
// application's middleware stored is hidden — read it back with collage.Varied,
// from the vary set, which is in the cache key. Deadline, cancellation and Err
// pass straight through to parent; only Value changes. In development it logs,
// once per render, when a hidden value is actually read, which is how an
// application discovers it has marked a page cacheable while a data handler reads
// something per-reader from the context.
func (h *Handler) renderSafeContext(parent context.Context) context.Context {
	c := renderSafe{Context: parent}
	if h.devMode {
		var once sync.Once
		c.warn = func() {
			once.Do(func() {
				h.logger.Warn("collage: a value stored in the request context was hidden from a shared render, " +
					"so a data handler read nothing where a per-request render would have seen the first reader's; " +
					"read a cache dimension with collage.Varied, or make the page Dynamic if its data is per-reader")
			})
		}
	}
	return c
}

// renderSafe is a context whose Value answers only for the framework's own
// request-independent keys, and nil for anything else — the value channel closed
// the way sharedRequest closes the header and body channels. It keeps one reader's
// context from reaching another's cached page from being possible at all, rather
// than trusting every data handler and middleware not to cause it.
type renderSafe struct {
	context.Context
	warn func() // dev-only; nil in production
}

func (c renderSafe) Value(key any) any {
	switch key.(type) {
	case routeCtxKey, varySetKey, chainStateKey, originsKey:
		return c.Context.Value(key)
	}
	// Not one of ours: hidden. Tell the developer, but only when the value was
	// actually there to hide — a lookup that would have missed anyway is no
	// mistake, and the context chain is walked for many keys that are simply absent.
	if c.warn != nil && c.Context.Value(key) != nil {
		c.warn()
	}
	return nil
}
