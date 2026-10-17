package httpx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/Elagoht/collage/internal/ascii"
	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/types"
)

// defaultMaxRecipes bounds the recipe store when Deps.MaxRecipes is left at zero.
// It matches the tracker's and the cache's default entry cap: a recipe is only
// worth keeping for a key the cache can still hold.
const defaultMaxRecipes = 10000

// errWarmNotStored is warmKey's error for a warm render that was served but left
// no fresh entry behind: another invalidation overlapped it, or the cache refused
// the write. Nothing about the upstream is known from it, unlike a failed render.
var errWarmNotStored = errors.New("collage: warm render was not stored")

// recipe is how to rebuild the request that wrote one cache key: exactly what the
// key was made of, so a warm render built from it writes that key and no other.
//
// It holds no reader's credentials. It is made from the request a shared render
// sees (see sharedRequest), which has already dropped everything the key does not
// hold.
type recipe struct {
	host, path string
	// rawPath is the request's own encoding of path, kept because the router
	// matches on the escaped path, not the decoded one.
	rawPath string
	// query is the query the key varies on, as written: the raw query of a route
	// that names no CacheParams, whose order is part of the key, and the
	// canonical selection of one that does. A url.Values would re-encode the
	// first in another order and so write another key.
	query string
	// vary is requestVary's entries, "Header=value", which the key holds.
	vary []string
	// header is the raw values of the declared vary headers. The key holds the
	// value a middleware resolved, but a shared render's request carries the
	// header itself, and a handler that reads it there must see what a
	// visitor's render saw.
	header   http.Header
	tls      bool
	document bool
}

// newRecipe records how to rebuild r, the request whose render was just written
// under a key built with cacheParams.
func newRecipe(r *http.Request, cacheParams []string, document bool) recipe {
	header := make(http.Header)
	for _, name := range requestVaryHeaders(r) {
		key := http.CanonicalHeaderKey(name)
		if values, ok := r.Header[key]; ok {
			header[key] = append([]string(nil), values...)
		}
	}
	return recipe{
		host:     r.Host,
		path:     r.URL.Path,
		rawPath:  r.URL.RawPath,
		query:    strings.Join(queryVary(r.URL, cacheParams), ""),
		vary:     requestVary(r),
		header:   header,
		tls:      r.TLS != nil,
		document: document,
	}
}

// request builds the GET the recipe describes, under ctx. Its vary set already
// holds the recipe's declarations, frozen, because no middleware runs to declare
// them: a warm render is the framework's, not a reader's.
func (rc recipe) request(ctx context.Context) *http.Request {
	set := &varySet{values: make(map[string]string, len(rc.vary)), frozen: true}
	for _, entry := range rc.vary {
		// A header name holds no "=", so the first one ends it.
		header, value, _ := strings.Cut(entry, "=")
		set.values[header] = value
		set.headers = append(set.headers, header)
	}
	ctx = context.WithValue(ctx, varySetKey{}, set)

	u := &url.URL{Scheme: "http", Host: rc.host, Path: rc.path, RawPath: rc.rawPath, RawQuery: rc.query}
	if rc.tls {
		u.Scheme = "https"
	}
	req := &http.Request{
		Method:     http.MethodGet,
		URL:        u,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     rc.header.Clone(),
		Body:       http.NoBody,
		Host:       rc.host,
		RequestURI: u.RequestURI(),
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if rc.tls {
		// Still TLS, so a scheme reads right, as sharedRequest keeps it.
		req.TLS = &tls.ConnectionState{}
	}
	return req.WithContext(ctx)
}

// recipeNode is one recipe in the store's recency list.
type recipeNode struct {
	key          string
	recipe       recipe
	newer, older *recipeNode
}

// recipes is the bounded store of warm recipes, one per cache key, in the order
// they were last written. It is in memory only, as the tracker is: after a restart
// an invalidation resolves no key until pages are written again, and so needs no
// recipe either.
type recipes struct {
	mu             sync.Mutex
	max            int
	byKey          map[string]*recipeNode
	newest, oldest *recipeNode
}

// newRecipes returns a store holding at most max recipes, defaultMaxRecipes when
// max is zero or less.
func newRecipes(max int) *recipes {
	if max <= 0 {
		max = defaultMaxRecipes
	}
	return &recipes{max: max, byKey: make(map[string]*recipeNode)}
}

// put records r as key's recipe, the newest one, evicting the oldest past the cap.
func (s *recipes) put(key string, r recipe) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if node, ok := s.byKey[key]; ok {
		node.recipe = r
		s.unlinkLocked(node)
		s.pushNewestLocked(node)
		return
	}
	node := &recipeNode{key: key, recipe: r}
	s.byKey[key] = node
	s.pushNewestLocked(node)
	if len(s.byKey) > s.max {
		evicted := s.oldest
		s.unlinkLocked(evicted)
		delete(s.byKey, evicted.key)
	}
}

// get returns key's recipe, and whether there is one.
func (s *recipes) get(key string) (recipe, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.byKey[key]
	if !ok {
		return recipe{}, false
	}
	return node.recipe, true
}

// forPath returns the keys whose recipe is for path on host, every variant of one
// URL. Hosts compare without case, as the cache key's does.
func (s *recipes) forPath(host, path string) []string {
	host = ascii.LowerString(host)
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for node := s.newest; node != nil; node = node.older {
		if node.recipe.path == path && ascii.LowerString(node.recipe.host) == host {
			keys = append(keys, node.key)
		}
	}
	return keys
}

// newestFirst returns those of keys that have a recipe, the most recently written
// first. A key with none cannot be warmed, so it is left out.
func (s *recipes) newestFirst(keys []string) []string {
	wanted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		wanted[key] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ordered := make([]string, 0, len(wanted))
	for node := s.newest; node != nil && len(ordered) < len(wanted); node = node.older {
		if _, ok := wanted[node.key]; ok {
			ordered = append(ordered, node.key)
		}
	}
	return ordered
}

func (s *recipes) pushNewestLocked(node *recipeNode) {
	node.newer, node.older = nil, s.newest
	if s.newest != nil {
		s.newest.newer = node
	}
	s.newest = node
	if s.oldest == nil {
		s.oldest = node
	}
}

func (s *recipes) unlinkLocked(node *recipeNode) {
	if node.newer != nil {
		node.newer.older = node.older
	} else {
		s.newest = node.older
	}
	if node.older != nil {
		node.older.newer = node.newer
	} else {
		s.oldest = node.newer
	}
	node.newer, node.older = nil, nil
}

// warmWriter is the ResponseWriter a warm render is served into. Nobody reads the
// body, so it is dropped; the status and the cache status are what the warm is
// judged by. A cookie a personalise step sets lands here too, and goes nowhere.
type warmWriter struct {
	header http.Header
	status int
}

func (w *warmWriter) Header() http.Header { return w.header }

func (w *warmWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *warmWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return len(p), nil
}

// warmKey re-renders the page or document cached under key from its recipe, so the
// entry an invalidation left stale is replaced before a visitor asks for it. ok is
// true when key is fresh in the cache afterwards. A key with no recipe, or a
// handler that does not read its cache (development, or no cache), is skipped with
// false and no error; a render that failed, or was served stale, is false with the
// reason.
//
// The render goes through serve, not ServeHTTP: ServeHTTP would run the request
// hooks and the application's and plugins' middleware, which must never see a
// request no reader sent — a rate limit, an access log, an auth check — and it
// gives every request a new, empty vary set, where a warm render needs the one the
// recipe holds. The rest of the lifecycle is a visitor's: the lookup, the flight,
// the stale fallback and the write rules. Its context is marked for fresh data, so
// the render waits for the upstream rather than storing a page made from stale
// data.
func (h *Handler) warmKey(ctx context.Context, key string) (ok bool, err error) {
	if h.devMode || h.cache == nil {
		return false, nil
	}
	rcp, found := h.recipes.get(key)
	if !found {
		return false, nil
	}

	route := &routeRef{}
	ctx = context.WithValue(ctx, routeCtxKey{}, route)
	if h.origins != nil {
		ctx = WithOrigins(ctx, h.origins)
	}
	req := rcp.request(types.WithFreshData(ctx))
	w := &warmWriter{header: make(http.Header)}
	status := h.serveWarm(w, req, route)

	where := rcp.host + rcp.path
	if status != http.StatusOK {
		return false, fmt.Errorf("collage: warm %s: status %d", where, status)
	}
	if w.header.Get(CacheStatusHeader) == cacheStale {
		return false, fmt.Errorf("collage: warm %s: the render failed or was degraded, the stale entry stays", where)
	}
	// Judged by the store, not the header: a degraded render goes out as a MISS
	// all the same, and only the entry says whether the key was written.
	var stored bool
	if staleCache, isStale := h.cache.(cache.StaleCache); isStale {
		_, _, stale, inCache := staleCache.GetStale(ctx, key)
		stored = inCache && !stale
	} else {
		_, _, stored = h.cache.Get(ctx, key)
	}
	if !stored {
		return false, fmt.Errorf("%w: %s", errWarmNotStored, where)
	}
	return true, nil
}

// serveWarm serves a warm request inside the panic guard serveGuarded gives a
// visitor's, without the middleware chain: a panic in an application's Cache or
// Router during a warm is a failed warm, answered and reported as a 500, not one
// that unwinds the goroutine warming.
func (h *Handler) serveWarm(w http.ResponseWriter, r *http.Request, route *routeRef) (status int) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		status = h.serveFailure(w, r, route.failure(
			http.StatusInternalServerError,
			stagePanic,
			fmt.Errorf("%w: %w", ErrPanic, &render.PanicError{Value: recovered, Stack: debug.Stack()}),
		))
	}()
	return h.serve(w, r, route)
}
