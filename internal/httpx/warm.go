package httpx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Elagoht/collage/internal/ascii"
	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/observability"
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

// warmOverlapError is warmKey's error for a warm render whose write an
// invalidation refused, having overlapped it. It is an errWarmNotStored, and
// carries the render's mark: an invalidation forgets the keys it reached, so this
// one is reached by none until it is written again, and the warmer queues it
// again itself (see requeue), bounded by the invalidations since that mark.
type warmOverlapError struct {
	where string
	mark  invalidationMark
}

func (e *warmOverlapError) Error() string {
	return errWarmNotStored.Error() + ": " + e.where + ": an invalidation overlapped it"
}

func (e *warmOverlapError) Is(target error) bool { return target == errWarmNotStored }

// errWarmGone is warmKey's error for a key whose content no longer exists: the
// render was a not found, which drops the entry, so its recipe goes too. A deleted
// post is the common case, and it is no sign that the upstream is down.
var errWarmGone = errors.New("collage: warmed content no longer exists")

// errWarmGuarded is warmKey's error for a guarded page, which is not warmed: a
// guard decides about a reader, and a warm is none.
var errWarmGuarded = errors.New("collage: a guarded page is not warmed")

// errWarmNotAPage is warmKey's error for a URL that now answers with a redirect
// or a refusal rather than the page its key holds: the routes changed, which is
// nothing to stop a run over.
var errWarmNotAPage = errors.New("collage: warmed URL no longer answers with its page")

// errWarmFresh is warmKey's error for a key found fresh already: a reader's
// request re-made it first. Nothing was warmed, and nothing is wrong.
var errWarmFresh = errors.New("collage: key was already fresh")

// errWarmEvicted is the warmer's error for a key an invalidation reached whose
// entry the cache no longer holds, not even stale: evicted, expired or dropped,
// so nobody is being served it. Warmed, it would be brought back for no reader;
// it is passed over, and its recipe dropped. A purge, which removes its entries
// itself, warms them all the same.
var errWarmEvicted = errors.New("collage: the cache no longer holds the key")

// errWarmUnread is the warmer's error for a key no reader has had since its last
// warm. Warming it again would keep a variant somebody asked for once warm for
// good; it is passed over, its recipe kept, until a reader has it again.
var errWarmUnread = errors.New("collage: nobody read the key since its last warm")

// errWarmShared is warmKey's error for a warm that joined another warm's render
// of the same key and found it failed: the warm that led it reports it.
var errWarmShared = errors.New("collage: warm joined a render that did not store")

// warmSkipped reports whether err, a false warmKey's reason, says nothing about
// the upstream, so a run goes on past it.
func warmSkipped(err error) bool {
	return err == nil || errors.Is(err, errWarmNotStored) || errors.Is(err, errWarmGone) ||
		errors.Is(err, errWarmGuarded) || errors.Is(err, errWarmNotAPage) || errors.Is(err, errWarmShared) ||
		errors.Is(err, errWarmFresh) || errors.Is(err, errWarmEvicted) || errors.Is(err, errWarmUnread)
}

// warmChecks are what a queued key is checked for before it is rendered, any of
// which passes it over.
type warmChecks uint8

const (
	// checkEntry passes over a key the cache no longer holds: errWarmEvicted.
	checkEntry warmChecks = 1 << iota
	// checkRead passes over a key no reader has had since its last warm:
	// errWarmUnread.
	checkRead

	// checksInvalidate are an invalidation's keys' checks.
	checksInvalidate = checkEntry | checkRead
	// checksPurge are a purge's: it removed the entries itself.
	checksPurge = checkRead
)

// warmJob is one key handed to a worker, with what it is checked for.
type warmJob struct {
	key    string
	checks warmChecks
}

// warmEligible checks key for checks before its render, and returns why it is
// passed over, or nil.
func (h *Handler) warmEligible(ctx context.Context, key string, checks warmChecks) error {
	if checks&checkRead != 0 && !h.recipes.readSinceWarm(key) {
		return fmt.Errorf("%w: %s", errWarmUnread, key)
	}
	if checks&checkEntry != 0 {
		// Only a store that keeps an entry past its invalidation can say the
		// cache let go of it: any other drops what an invalidation reaches, and
		// every key would read as gone.
		if staleCache, ok := h.cache.(cache.StaleCache); ok {
			if _, _, _, found := staleCache.GetStale(ctx, key); !found {
				h.recipes.drop(key)
				return fmt.Errorf("%w: %s", errWarmEvicted, key)
			}
		}
	}
	return nil
}

// warmCtxKey marks a warm request's context with its *warmState. Unexported, so
// it is set only by warmKey, and no request from the wire can carry it.
type warmCtxKey struct{}

// warmState is what one warm request's quiet paths leave for warmKey to judge it
// by. A warm logs nothing per page and calls no error hook — the warmer reports
// its run as a whole — so the cause of a failure is kept here instead.
type warmState struct {
	mu       sync.Mutex
	cause    error
	skipped  error
	degraded bool
	shared   bool
	// overlapped is set when an invalidation refused the render's write, or
	// dropped the entry just written; mark is when that render began.
	overlapped bool
	mark       invalidationMark
	// writeErr is the render's cache write or tracking failure. Unlike any
	// other recorded cause it fails a warm that otherwise succeeded: an entry
	// not written is a page left stale, and one written but not tracked is
	// fresh and out of every invalidation's reach.
	writeErr error
}

// warmStateOf returns ctx's warm state, or nil for any request but a warm.
func warmStateOf(ctx context.Context) *warmState {
	state, _ := ctx.Value(warmCtxKey{}).(*warmState)
	return state
}

// fail records err as the warm's failure, keeping the first: it is the cause,
// and what follows from it — an error page that could not be served — is not.
func (s *warmState) fail(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cause == nil {
		s.cause = err
	}
}

// writeFailed records the failure of the render's cache write or tracking.
func (s *warmState) writeFailed(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr == nil {
		s.writeErr = err
	}
}

// overlap records that an invalidation overlapped the render begun at mark, so
// its entry was not kept.
func (s *warmState) overlap(mark invalidationMark) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.overlapped, s.mark = true, mark
}

// overlapMark returns the mark overlap recorded, and whether it did.
func (s *warmState) overlapMark() (invalidationMark, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mark, s.overlapped
}

// skip records that the warm was not attempted, and why.
func (s *warmState) skip(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipped = err
}

// observe records the outcome of the render the warm was answered from: whether
// it was degraded, which the response alone cannot say once there is no stale
// entry to serve in its place, and whether it was another warm's.
func (s *warmState) observe(out *outcome, shared bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.degraded = out.fail == nil && out.degraded
	s.shared = shared
	if out.fail != nil && out.fail.err != nil && s.cause == nil {
		s.cause = out.fail.err
	}
}

// record records the failure behind a stale serve, for reportStale.
func (s *warmState) record(out *outcome) {
	if out.fail != nil {
		s.fail(out.fail.err)
	}
}

func (s *warmState) result() (cause, skipped, writeErr error, degraded, shared bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cause, s.skipped, s.writeErr, s.degraded, s.shared
}

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
	// cacheParams is the route's CacheParams the key was built with, nil for a
	// route that names none, so the query a URL's key is made from can be
	// worked out again for an action's redirect target (see forURL).
	cacheParams []string
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
		// Cloned, keeping a nil nil: queryVary tells "no CacheParams" from
		// "an empty list" by it.
		cacheParams: slices.Clone(cacheParams),
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
	key    string
	recipe recipe
	// read is set when a reader has had the key since its last warm: served it
	// fresh or stale, or written it. A warm's write clears it, and a warm
	// passes over a key whose flag is clear. Atomic, as it is set on a reader's
	// hit under the store's read lock only.
	read         atomic.Bool
	newer, older *recipeNode
}

// recipes is the bounded store of warm recipes, one per cache key, in the order
// they were last written. It is in memory only, as the tracker is: after a restart
// an invalidation resolves no key until pages are written again, and so needs no
// recipe either.
type recipes struct {
	// mu is a read-write lock so the readers' hits that mark a key read, under
	// its read lock, do not wait on one another.
	mu             sync.RWMutex
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
// read is true when a reader's render wrote the key, which marks it read; a warm's
// write leaves the mark alone, for clearRead once the write has stood. A new
// recipe is marked read either way.
func (s *recipes) put(key string, r recipe, read bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if node, ok := s.byKey[key]; ok {
		node.recipe = r
		if read {
			node.read.Store(true)
		}
		s.unlinkLocked(node)
		s.pushNewestLocked(node)
		return
	}
	node := &recipeNode{key: key, recipe: r}
	node.read.Store(true)
	s.byKey[key] = node
	s.pushNewestLocked(node)
	if len(s.byKey) > s.max {
		evicted := s.oldest
		s.unlinkLocked(evicted)
		delete(s.byKey, evicted.key)
	}
}

// markRead marks key read by a reader. It is on every reader's hit, so it takes
// the read lock only, and writes the flag only when it is not set already: a
// popular key's flag is then only ever read.
func (s *recipes) markRead(key string) {
	s.mu.RLock()
	node := s.byKey[key]
	s.mu.RUnlock()
	// A node evicted meanwhile is marked for nobody, harmlessly.
	if node != nil && !node.read.Load() {
		node.read.Store(true)
	}
}

// clearRead clears key's read mark, once a warm's write of it has stood.
func (s *recipes) clearRead(key string) {
	s.mu.RLock()
	node := s.byKey[key]
	s.mu.RUnlock()
	if node != nil {
		node.read.Store(false)
	}
}

// readSinceWarm reports whether a reader has had key since its last warm. A key
// with no recipe answers true: there is nothing to pass over, and warmKey skips
// it on its own.
func (s *recipes) readSinceWarm(key string) bool {
	s.mu.RLock()
	node := s.byKey[key]
	s.mu.RUnlock()
	return node == nil || node.read.Load()
}

// get returns key's recipe, and whether there is one.
func (s *recipes) get(key string) (recipe, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	node, ok := s.byKey[key]
	if !ok {
		return recipe{}, false
	}
	return node.recipe, true
}

// drop forgets key's recipe.
func (s *recipes) drop(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if node, ok := s.byKey[key]; ok {
		s.unlinkLocked(node)
		delete(s.byKey, key)
	}
}

// forURL returns the keys a GET of target on host would be looked up under,
// every vary-header variant of it: those whose recipe is for target's path, and
// whose query is the one target's key is built from — its raw query on a route
// that names no CacheParams, the canonical selection on one that does, exactly as
// the cache key works it out.
//
// The one whose vary entries are vary, if there is one, comes first, and matched
// says there is: it is the variant a request declaring vary lands on.
func (s *recipes) forURL(host string, target *url.URL, vary []string) (keys []string, matched bool) {
	host = ascii.LowerString(host)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for node := s.newest; node != nil; node = node.older {
		rc := node.recipe
		if rc.path == target.Path && ascii.LowerString(rc.host) == host &&
			strings.Join(queryVary(target, rc.cacheParams), "") == rc.query {
			keys = append(keys, node.key)
			if !matched && slices.Equal(rc.vary, vary) {
				matched = true
				last := len(keys) - 1
				keys[0], keys[last] = keys[last], keys[0]
			}
		}
	}
	return keys, matched
}

// newestFirst returns those of keys that have a recipe, the most recently written
// first. A key with none cannot be warmed, so it is left out.
func (s *recipes) newestFirst(keys []string) []string {
	wanted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		wanted[key] = struct{}{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
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
// false and no error.
//
// Otherwise a false comes with the reason, and the reason says whether the run
// should stop (see warmSkipped): a render that failed, or was degraded, says the
// upstream is in trouble; content that is gone, a guarded page, a page that no
// longer answers with itself, or a render another invalidation overlapped says
// nothing about it.
//
// The render goes through serve, not ServeHTTP: ServeHTTP would run the request
// hooks and the application's and plugins' middleware, which must never see a
// request no reader sent — a rate limit, an access log, an auth check — and it
// gives every request a new, empty vary set, where a warm render needs the one the
// recipe holds. The rest of the lifecycle is a visitor's: the lookup, the stale
// fallback and the write rules, but in a flight of its own (see warmFlight), with
// no personalisation, and quiet: its failures are recorded in its warmState for
// the warmer to report, not logged per page. Its context is marked for fresh data,
// so the render waits for the upstream rather than storing a page made from stale
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
	state := &warmState{}
	ctx = context.WithValue(ctx, routeCtxKey{}, route)
	ctx = context.WithValue(ctx, warmCtxKey{}, state)
	if h.origins != nil {
		ctx = WithOrigins(ctx, h.origins)
	}
	req := rcp.request(types.WithFreshData(ctx))
	w := &warmWriter{header: make(http.Header)}
	status := h.serveWarm(w, req, route)

	where := rcp.host + rcp.path
	cause, skip, writeErr, degraded, shared := state.result()
	switch {
	case skip != nil:
		if errors.Is(skip, errWarmGuarded) {
			// It never will be warmed, so later invalidations need not queue it.
			h.recipes.drop(key)
		}
		return false, fmt.Errorf("%w: %s", skip, where)
	case status == http.StatusNotFound:
		h.recipes.drop(key)
		return false, fmt.Errorf("%w: %s", errWarmGone, where)
	case status != http.StatusOK && status < http.StatusInternalServerError:
		// A redirect, a refusal: the URL no longer answers with the page the key
		// holds, and its recipe can never warm it again.
		h.recipes.drop(key)
		return false, fmt.Errorf("%w: %s: status %d", errWarmNotAPage, where, status)
	case status != http.StatusOK || w.header.Get(CacheStatusHeader) == cacheStale || degraded:
		if shared {
			// Another warm's render, which that warm reports.
			return false, fmt.Errorf("%w: %s", errWarmShared, where)
		}
		if cause == nil {
			cause = errors.New("a fragment failed")
		}
		return false, fmt.Errorf("collage: warm %s: the render failed or was degraded, the stale entry stays: %w", where, cause)
	case writeErr != nil:
		// A failure, not a skip: the cache or the tracker is failing, which the
		// next warm would run into as well.
		return false, fmt.Errorf("collage: warm %s: %w", where, writeErr)
	case w.header.Get(CacheStatusHeader) == cacheHit:
		return false, fmt.Errorf("%w: %s", errWarmFresh, where)
	}
	// Judged by the store, not the header: a render another invalidation
	// overlapped goes out as a MISS all the same, and only the entry says
	// whether the key was written.
	var stored bool
	if staleCache, isStale := h.cache.(cache.StaleCache); isStale {
		_, _, stale, inCache := staleCache.GetStale(ctx, key)
		stored = inCache && !stale
	} else {
		_, _, stored = h.cache.Get(ctx, key)
	}
	if !stored {
		// Told apart from a cache that dropped the write on its own: only an
		// overlap is warmed again, as only an overlap has an invalidation that
		// bounds how often.
		if mark, overlapped := state.overlapMark(); overlapped {
			return false, &warmOverlapError{where: where, mark: mark}
		}
		if cause != nil {
			return false, fmt.Errorf("%w: %s: %w", errWarmNotStored, where, cause)
		}
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

// warmWorkers is how many warm renders run at once. Enough to re-make a few
// hundred pages in well under a second when their data is cached, few enough
// that a warm never crowds the visitors it is warming for.
const warmWorkers = 4

// defaultWarmLimit is a run's cap when Deps.WarmLimit is zero.
const defaultWarmLimit = 1000

// warmer re-renders invalidated keys in the background: one coordinator takes
// runs, one per Warm call, in order, and hands each run's keys to warmWorkers
// workers. The goroutines start with the first Warm, so an application that never
// invalidates never has them.
type warmer struct {
	limit int
	// targetBudget is actionWarmBudget, a field so a test can shorten it.
	targetBudget time.Duration

	mu   sync.Mutex
	idle *sync.Cond
	// pending holds every key queued and not yet handed to a worker. A key
	// leaves it when its render starts, not when it ends: an invalidation
	// during that render keeps the render from being stored, and must be able
	// to queue the key again.
	// Its value is what the key is checked for before it renders: queued by
	// more than one call, the fewest checks of them.
	pending map[string]warmChecks
	// parked holds the keys whose warm an invalidation overlapped, waiting for
	// no invalidation to be under way to be queued again: rendered while one
	// is, they would be refused again. requeued is each such key's bound, kept
	// until the key's warm ends some other way. See requeue.
	parked   map[string]struct{}
	requeued map[string]requeueCount
	// runs are the runs not yet started, oldest first. A key of a run that is no
	// longer pending — dropped by a stop — is passed over.
	runs [][]string
	// running is true while the coordinator works through a run.
	running bool
	started bool
	stopped bool

	// wake tells the coordinator a run was queued. Buffered by one, and sent
	// to without blocking: Warm runs on an invalidating caller's goroutine,
	// often an action's request, which must never wait on the warmer.
	wake chan struct{}
	// stop is closed by StopWarming.
	stop chan struct{}
	// ctx is the warm renders' context. It is no reader's, so nothing ends it
	// but StopWarming giving up on the renders still running.
	ctx    context.Context
	cancel context.CancelFunc
	// done counts the coordinator, the workers, and every action's warm of its
	// redirect target under way.
	done sync.WaitGroup
}

// newWarmer returns a warmer capping each run at limit keys, defaultWarmLimit
// when limit is zero.
func newWarmer(limit int) *warmer {
	if limit == 0 {
		limit = defaultWarmLimit
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &warmer{
		limit:        limit,
		targetBudget: actionWarmBudget,
		pending:      make(map[string]warmChecks),
		parked:       make(map[string]struct{}),
		requeued:     make(map[string]requeueCount),
		wake:         make(chan struct{}, 1),
		stop:         make(chan struct{}),
		ctx:          ctx,
		cancel:       cancel,
	}
	w.idle = sync.NewCond(&w.mu)
	return w
}

// warmOutcome is how one key's warm ended, as a run counts it.
type warmOutcome int

const (
	warmDone warmOutcome = iota
	warmSkip
	warmFail
)

// warmResult is one key's warm, from a worker to the coordinator.
type warmResult struct {
	outcome warmOutcome
	err     error
}

// Warm queues keys to be re-rendered in the background, and returns at once. It
// is what an invalidation calls, after it has finished, with the keys it reached,
// so each page is fresh again before its first reader asks — who is meanwhile
// served the stale page, never made to wait for this.
//
// The keys become one run: those with a recipe, the most recently written first,
// less those already queued, capped at the warm limit. A key over the cap, or
// with no recipe, is not warmed, and renders on its first request as it would
// have without warming. A run stops at its first failed render: see runWarm.
//
// Before it renders, a key is passed over when the cache no longer holds it (see
// errWarmEvicted), or no reader has had it since its last warm (errWarmUnread).
//
// Warm does nothing when warming is off, and nothing after StopWarming.
func (h *Handler) Warm(keys []string) {
	h.warmKeys(keys, checksInvalidate)
}

// WarmPurged is Warm for the keys a purge reached, which removed their entries
// itself: they are warmed though the cache no longer holds them.
func (h *Handler) WarmPurged(keys []string) {
	h.warmKeys(keys, checksPurge)
}

// warmKeys is Warm and WarmPurged, checking each key for checks.
func (h *Handler) warmKeys(keys []string, checks warmChecks) {
	w := h.warm
	if w == nil {
		return
	}
	// Before the warmer's lock, which the coordinator takes between renders: the
	// recipe store has a lock of its own.
	ordered := h.recipes.newestFirst(keys)
	// Every invalidation calls Warm once over, those that reached no key too:
	// that is when the keys parked behind it are queued again, unless another
	// is still under way, whose own Warm will.
	quiet := h.noInvalidationUnderWay()

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	run := make([]string, 0, min(len(ordered), w.limit))
	for _, key := range ordered {
		if len(run) == w.limit {
			break
		}
		if queued, ok := w.pending[key]; ok {
			w.pending[key] = queued & checks
			continue
		}
		w.pending[key] = checks
		run = append(run, key)
	}
	if quiet {
		run = w.unparkLocked(run)
	}
	h.queueRunLocked(run)
}

// queueRunLocked queues run, starting the goroutines with the first, and wakes
// the coordinator. Under w.mu.
func (h *Handler) queueRunLocked(run []string) {
	w := h.warm
	if len(run) == 0 {
		return
	}
	w.runs = append(w.runs, run)
	if !w.started {
		w.started = true
		jobs := make(chan warmJob)
		results := make(chan warmResult)
		w.done.Add(1 + warmWorkers)
		go h.coordinateWarm(w, jobs, results)
		for range warmWorkers {
			go h.warmWorker(w, jobs, results)
		}
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// StopWarming stops the warmer, for shutdown: the queue is dropped, no run is
// started again, and every later Warm does nothing. It waits for the renders
// still running to finish, or for ctx to end, whichever is first; past ctx, the
// renders' context is cancelled, and they end on their own after it returns. It
// is safe to call more than once, and on a handler that never warmed.
func (h *Handler) StopWarming(ctx context.Context) {
	w := h.warm
	if w == nil {
		return
	}
	w.mu.Lock()
	if !w.stopped {
		w.stopped = true
		clear(w.pending)
		clear(w.parked)
		clear(w.requeued)
		w.runs = nil
		close(w.stop)
		w.idle.Broadcast()
	}
	w.mu.Unlock()

	// Waited for even when no run ever started: an action's warm of its
	// redirect target is counted in done too (see begin).
	finished := make(chan struct{})
	go func() {
		w.done.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-ctx.Done():
	}
	// The data cache closes after this, and a render still running would read
	// from it: told to stop, it does.
	w.cancel()
}

// WaitWarmed blocks until the warmer has nothing queued and no run under way. It
// is for tests and tools that must see the cache after a warm, and returns at once
// when warming is off.
func (h *Handler) WaitWarmed() {
	w := h.warm
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for len(w.runs) > 0 || w.running || len(w.parked) > 0 {
		w.idle.Wait()
	}
}

// requeueCount is how often a key has been queued again after an overlapped
// warm, and the finished count when the first such warm began: the
// invalidations not finished by then, under way or begun since, are those
// allowed to have overlapped it, one re-queue each.
type requeueCount struct {
	base, count uint64
}

// noInvalidationUnderWay reports whether no invalidation is under way now.
func (h *Handler) noInvalidationUnderWay() bool {
	m := h.invalidationMark()
	return m.started == m.finished
}

// requeue queues key again after its warm render, begun at mark, was refused its
// write by an invalidation that overlapped it. The invalidation forgot the key,
// as every invalidation forgets what it reached, and the refused write did not
// track it again, so no later invalidation would reach it: without this it
// stays stale until its next reader.
//
// It is parked until no invalidation is under way, when either the invalidation
// ending queues it (see Warm), or, already over, this does at once. Parked
// before the counts are read, so an invalidation ending between the two still
// finds it.
//
// Bounded, so a storm of invalidations cannot keep one key rendering: see park.
func (h *Handler) requeue(key string, mark invalidationMark) {
	w := h.warm
	if !w.park(key, mark, h.invalidationMark().started) {
		return
	}
	if !h.noInvalidationUnderWay() {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	h.queueRunLocked(w.unparkLocked(nil))
}

// park parks key for requeue, and reports whether it did: at most once per
// invalidation started, as of started, since the first of its refused warms
// began at mark — any later refusal needs an invalidation of its own to have
// overlapped it. Past that the key is let go, stale until its next reader.
func (w *warmer) park(key string, mark invalidationMark, started uint64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return false
	}
	count, ok := w.requeued[key]
	if !ok {
		count = requeueCount{base: mark.finished}
	}
	// base is a finished count read no later than started was, so never
	// above it.
	if count.count >= started-count.base {
		w.settleLocked(key)
		return false
	}
	count.count++
	w.requeued[key] = count
	w.parked[key] = struct{}{}
	return true
}

// unparkLocked appends the parked keys not already queued to run, and queues
// them. Under w.mu.
func (w *warmer) unparkLocked(run []string) []string {
	for key := range w.parked {
		delete(w.parked, key)
		// Checked for nothing: it passed its checks before the render that
		// was refused, whose drop of the entry and clearing of nothing are
		// the overlap's, not the cache's or the readers'.
		if _, queued := w.pending[key]; queued {
			w.pending[key] = 0
			continue
		}
		w.pending[key] = 0
		run = append(run, key)
	}
	return run
}

// settle forgets key's re-queue bound, once its warm has ended some other way
// than overlapped.
func (w *warmer) settle(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.settleLocked(key)
}

func (w *warmer) settleLocked(key string) {
	delete(w.requeued, key)
	if _, parked := w.parked[key]; parked {
		delete(w.parked, key)
		w.idle.Broadcast()
	}
}

// coordinateWarm is the warmer's coordinator: it takes one run at a time until
// StopWarming, and closes jobs when it leaves, which ends the workers.
func (h *Handler) coordinateWarm(w *warmer, jobs chan<- warmJob, results <-chan warmResult) {
	defer w.done.Done()
	defer close(jobs)
	for {
		w.mu.Lock()
		if w.stopped {
			w.mu.Unlock()
			return
		}
		if len(w.runs) == 0 {
			w.mu.Unlock()
			select {
			case <-w.wake:
			case <-w.stop:
			}
			continue
		}
		run := w.runs[0]
		w.runs = w.runs[1:]
		w.running = true
		w.mu.Unlock()

		h.runWarm(w, run, jobs, results)

		w.mu.Lock()
		w.running = false
		w.idle.Broadcast()
		w.mu.Unlock()
	}
}

// claim takes the next key of run at or after i that is still pending, and
// returns it, with its checks, and the index after it; a job with no key when
// there is none.
func (w *warmer) claim(run []string, i int) (warmJob, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for ; i < len(run); i++ {
		if checks, queued := w.pending[run[i]]; queued {
			delete(w.pending, run[i])
			return warmJob{key: run[i], checks: checks}, i + 1
		}
	}
	return warmJob{}, i
}

// release lets go of the keys of run from i on that are still pending, and
// returns how many there were.
func (w *warmer) release(run []string, i int) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for ; i < len(run); i++ {
		if _, queued := w.pending[run[i]]; queued {
			delete(w.pending, run[i])
			delete(w.requeued, run[i])
			n++
		}
	}
	return n
}

// runWarm warms run's keys with the workers.
//
// The first key renders on its own, and the rest only once it is fresh: a down
// upstream then costs one failed render, not one per worker. At the first
// failure the run stops — a page that failed most likely failed for a reason the
// next one shares, and an upstream that is down is not to be hammered — and the
// rest of it is dropped, stale until its first request, with one warn. The renders
// already running are waited for: a worker's result has nowhere else to go.
func (h *Handler) runWarm(w *warmer, run []string, jobs chan<- warmJob, results <-chan warmResult) {
	began := time.Now()
	var warmed, skipped, failed int
	outstanding, next := 0, 0
	// probing holds the fan-out until a render has succeeded: a skipped key says
	// nothing about the upstream either way.
	probing, stopping := true, false
	var held warmJob
	stop := w.stop
	// stopped is what StopWarming does to the run, seen from either select:
	// nothing more is handed out, and what was not, held key included, is
	// counted skipped.
	stopped := func() {
		stop = nil
		if stopping {
			return
		}
		stopping = true
		skipped += len(run) - next
		if held.key != "" {
			skipped++
			held = warmJob{}
		}
	}
	for {
		// Checked first, not only beside the send below: with both ready, the
		// select would pick at random, and hand out one more key after the
		// queue was dropped about half the time.
		if stop != nil {
			select {
			case <-stop:
				stopped()
			default:
			}
		}
		if held.key == "" && !stopping && (!probing || outstanding == 0) {
			held, next = w.claim(run, next)
		}
		if held.key == "" && outstanding == 0 {
			break
		}
		var send chan<- warmJob
		if held.key != "" {
			send = jobs
		}
		select {
		case send <- held:
			held = warmJob{}
			outstanding++
		case result := <-results:
			outstanding--
			switch result.outcome {
			case warmDone:
				warmed++
				probing = false
			case warmSkip:
				skipped++
			case warmFail:
				failed++
				if !stopping {
					stopping = true
					dropped := w.release(run, next)
					if held.key != "" {
						dropped++
						held = warmJob{}
					}
					skipped += dropped
					h.logger.Warn("collage: warming stopped, a page failed to render",
						"err", result.err, "remaining", dropped, "run", len(run))
				}
			}
		case <-stop:
			// Shutdown: what is not running is dropped, without a warn — it is
			// no failure — and StopWarming has let go of the queue already. The
			// renders running are still waited for, above.
			stopped()
		}
	}
	h.logger.Info("collage: warmed pages", "warmed", warmed, "skipped", skipped, "failed", failed,
		"run", len(run), "duration", time.Since(began))
}

// warmWorker warms the keys handed to it until jobs is closed.
func (h *Handler) warmWorker(w *warmer, jobs <-chan warmJob, results chan<- warmResult) {
	defer w.done.Done()
	for job := range jobs {
		results <- h.warmOne(w.ctx, job.key, job.checks)
	}
}

// warmOne warms key, once it passes checks (see warmEligible), and says how the
// run is to count it. A panic anywhere in it —
// serveWarm guards the serve, but not the store check after it or the metric —
// is a failed warm, never a worker lost with its result.
func (h *Handler) warmOne(ctx context.Context, key string, checks warmChecks) (result warmResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = warmResult{outcome: warmFail, err: fmt.Errorf("%w: %w", ErrPanic,
				&render.PanicError{Value: recovered, Stack: debug.Stack()})}
		}
	}()
	if err := h.warmEligible(ctx, key, checks); err != nil {
		h.warm.settle(key)
		return warmResult{outcome: warmSkip, err: err}
	}
	ok, err := h.warmKey(ctx, key)
	// Before the result is handed on, so a run is not over, nor the warmer
	// idle, while the key is still to be queued again.
	var overlap *warmOverlapError
	if errors.As(err, &overlap) && ctx.Err() == nil {
		h.requeue(key, overlap.mark)
	} else {
		h.warm.settle(key)
	}
	switch {
	case ok:
		h.metrics.CacheEvent(ctx, observability.CacheWarm, key)
		return warmResult{outcome: warmDone}
	case warmSkipped(err), ctx.Err() != nil:
		// A warm StopWarming cut short is no failure of the page's.
		return warmResult{outcome: warmSkip, err: err}
	}
	return warmResult{outcome: warmFail, err: err}
}

// actionWarmBudget is the longest an action's response waits for its redirect
// target to be warmed. A request deadline, when the request has one, cuts it
// shorter. Past it the redirect goes out, the render under way finishes on its
// own, and the keys not yet taken are left to the background run: the submit is
// the editor's, and is not to be held on a slow upstream.
const actionWarmBudget = 5 * time.Second

// take takes key out of the queue, so the background run passes it over, and
// reports whether the warmer is still running: after StopWarming nothing is
// warmed, not even an action's redirect target.
func (w *warmer) take(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.pending, key)
	return !w.stopped
}

// begin counts one action's warm of its redirect target among the goroutines
// StopWarming waits for, and reports false, counting nothing, once the warmer is
// stopped. Under w.mu, as Warm's own Add is: every Add then comes before
// StopWarming's Wait.
func (w *warmer) begin() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return false
	}
	w.done.Add(1)
	return true
}

// warmRedirectTarget warms the page an action's response redirects to, before the
// response goes out, so the reader who submitted the form lands on a fresh HIT
// with what they just changed rather than waiting on its render. It is called
// after the action's invalidation, which has queued the same keys for the
// background run.
//
// Only the keys the redirected request can land on are warmed: the target's path,
// with the query that request's key will be built from (see recipes.forURL) —
// every vary-header variant of it, since which one the reader is the request
// alone does not say here. Every other variant of the path stays in the
// background run.
//
// The keys are warmed one at a time, each taken out of the queue just before,
// so it is not rendered twice; taking one the background run has claimed already
// is harmless, as the two renders coalesce in warmFlight or one finds the
// other's entry fresh. The wait is held to actionWarmBudget: past it the
// response goes out and the keys not taken are left in the queue.
//
// The renders run under the warmer's context, not the request's: detached from
// the client going away, and carrying none of the values an auth middleware or
// the action left on the request's, which must not reach a shared entry. A failed
// render, or a joined one that failed, ends it at once — the upstream is in
// trouble, and the background run reports it — logged at debug only: the page
// stays stale, and the redirect goes out all the same.
func (h *Handler) warmRedirectTarget(r *http.Request, action, location string) {
	w := h.warm
	if w == nil {
		return
	}
	target, ok := sameOriginTarget(r, location)
	if !ok {
		return
	}
	// The variant the submitter lands on first: the redirected request carries
	// the headers this one did, so it declares the same vary entries.
	keys, matched := h.recipes.forURL(r.Host, target, requestVary(r))
	if len(keys) == 0 {
		return
	}
	if len(keys) > w.limit {
		keys = keys[:w.limit]
	}
	jobs := make([]warmJob, len(keys))
	for i, key := range keys {
		jobs[i] = warmJob{key: key, checks: checksInvalidate}
	}
	if matched {
		// Read next, by the reader following the redirect: warmed whether or
		// not anyone read it since its last warm, or the cache still holds it.
		jobs[0].checks = 0
	}
	deadline := time.Now().Add(w.targetBudget)
	if requestDeadline, set := r.Context().Deadline(); set && requestDeadline.Before(deadline) {
		deadline = requestDeadline
	}
	if !w.begin() {
		return
	}
	finished := make(chan struct{})
	go func() {
		defer w.done.Done()
		defer close(finished)
		h.warmTargetKeys(w, action, target.Path, jobs, deadline)
	}()
	budget := time.NewTimer(time.Until(deadline))
	defer budget.Stop()
	select {
	case <-finished:
	case <-budget.C:
		// The render under way goes on in the goroutine, which takes no
		// further key: what is left is the background run's.
		h.logger.Debug("collage: an action's redirect target was not warmed within its budget",
			"action", action, "path", target.Path, "budget", w.targetBudget)
	}
}

// warmTargetKeys is warmRedirectTarget's loop, run in a goroutine of its own so
// the response need not outlast the budget even while a render does.
func (h *Handler) warmTargetKeys(w *warmer, action, path string, jobs []warmJob, deadline time.Time) {
	for _, job := range jobs {
		key := job.key
		if !time.Now().Before(deadline) || !w.take(key) {
			return
		}
		result := h.warmOne(w.ctx, key, job.checks)
		if result.outcome == warmSkip && w.ctx.Err() == nil && errors.Is(result.err, errWarmNotStored) &&
			time.Now().Before(deadline) {
			// It joined a render that began before this action's invalidation,
			// whose write the invalidation then refused. That flight is over, so
			// this one leads a render of its own; without it the key, out of the
			// queue, stays stale.
			result = h.warmOne(w.ctx, key, 0)
		}
		switch {
		case result.outcome == warmFail, errors.Is(result.err, errWarmShared):
			// A joined render that failed is as much the upstream in trouble as a
			// render of its own that did: rendering on would hold the submit for
			// another fragment timeout each.
			h.logger.Debug("collage: an action's redirect target was not warmed, it stays stale",
				"action", action, "path", path, "err", result.err)
			return
		case result.err != nil:
			h.logger.Debug("collage: an action's redirect target was skipped by warming",
				"action", action, "path", path, "err", result.err)
		}
	}
}

// sameOriginTarget returns the URL location names on r's own origin: a relative
// reference resolved against r's URL, or an absolute http or https URL naming r's
// host. Anything else — another host, another scheme, an unparsable location — is
// no page of this cache, and ok is false. The fragment is no part of any key, and
// is dropped.
func sameOriginTarget(r *http.Request, location string) (target *url.URL, ok bool) {
	loc, err := url.Parse(location)
	if err != nil || loc.Opaque != "" {
		return nil, false
	}
	if loc.Scheme != "" || loc.Host != "" {
		// url.Parse lowercases the scheme; a host compares without case, as the
		// cache key's does. A scheme with no host ("http:/p") is left alone:
		// what a browser makes of it is not worth guessing at.
		if loc.Scheme != "" && loc.Scheme != "http" && loc.Scheme != "https" {
			return nil, false
		}
		if loc.Host == "" || ascii.LowerString(loc.Host) != ascii.LowerString(r.Host) {
			return nil, false
		}
	}
	target = r.URL.ResolveReference(loc)
	target.Fragment, target.RawFragment = "", ""
	if target.Path == "" {
		target.Path = "/"
	}
	return target, true
}
