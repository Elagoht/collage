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

// errWarmShared is warmKey's error for a warm that joined another warm's render
// of the same key and found it failed: the warm that led it reports it.
var errWarmShared = errors.New("collage: warm joined a render that did not store")

// warmSkipped reports whether err, a false warmKey's reason, says nothing about
// the upstream, so a run goes on past it.
func warmSkipped(err error) bool {
	return err == nil || errors.Is(err, errWarmNotStored) || errors.Is(err, errWarmGone) ||
		errors.Is(err, errWarmGuarded) || errors.Is(err, errWarmNotAPage) || errors.Is(err, errWarmShared) ||
		errors.Is(err, errWarmFresh)
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

// drop forgets key's recipe.
func (s *recipes) drop(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if node, ok := s.byKey[key]; ok {
		s.unlinkLocked(node)
		delete(s.byKey, key)
	}
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

	mu   sync.Mutex
	idle *sync.Cond
	// pending holds every key queued and not yet handed to a worker. A key
	// leaves it when its render starts, not when it ends: an invalidation
	// during that render keeps the render from being stored, and must be able
	// to queue the key again.
	pending map[string]struct{}
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
	// done counts the coordinator and the workers.
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
		limit:   limit,
		pending: make(map[string]struct{}),
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		ctx:     ctx,
		cancel:  cancel,
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
// Warm does nothing when warming is off, and nothing after StopWarming.
func (h *Handler) Warm(keys []string) {
	w := h.warm
	if w == nil || len(keys) == 0 {
		return
	}
	// Before the warmer's lock, which the coordinator takes between renders: the
	// recipe store has a lock of its own.
	ordered := h.recipes.newestFirst(keys)

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
		if _, queued := w.pending[key]; queued {
			continue
		}
		w.pending[key] = struct{}{}
		run = append(run, key)
	}
	if len(run) == 0 {
		return
	}
	w.runs = append(w.runs, run)
	if !w.started {
		w.started = true
		jobs := make(chan string)
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
		w.runs = nil
		close(w.stop)
		w.idle.Broadcast()
	}
	started := w.started
	w.mu.Unlock()

	if started {
		finished := make(chan struct{})
		go func() {
			w.done.Wait()
			close(finished)
		}()
		select {
		case <-finished:
		case <-ctx.Done():
		}
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
	for len(w.runs) > 0 || w.running {
		w.idle.Wait()
	}
}

// coordinateWarm is the warmer's coordinator: it takes one run at a time until
// StopWarming, and closes jobs when it leaves, which ends the workers.
func (h *Handler) coordinateWarm(w *warmer, jobs chan<- string, results <-chan warmResult) {
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
// returns it with the index after it; "" when there is none.
func (w *warmer) claim(run []string, i int) (string, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for ; i < len(run); i++ {
		if _, queued := w.pending[run[i]]; queued {
			delete(w.pending, run[i])
			return run[i], i + 1
		}
	}
	return "", i
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
func (h *Handler) runWarm(w *warmer, run []string, jobs chan<- string, results <-chan warmResult) {
	began := time.Now()
	var warmed, skipped, failed int
	outstanding, next := 0, 0
	// probing holds the fan-out until a render has succeeded: a skipped key says
	// nothing about the upstream either way.
	probing, stopping := true, false
	held := ""
	stop := w.stop
	for {
		// Checked first, not only beside the send below: with both ready, the
		// select would pick at random, and hand out one more key after the
		// queue was dropped about half the time.
		if stop != nil {
			select {
			case <-stop:
				stop = nil
				if !stopping {
					stopping = true
					skipped += len(run) - next
					if held != "" {
						skipped++
						held = ""
					}
				}
			default:
			}
		}
		if held == "" && !stopping && (!probing || outstanding == 0) {
			held, next = w.claim(run, next)
		}
		if held == "" && outstanding == 0 {
			break
		}
		var send chan<- string
		if held != "" {
			send = jobs
		}
		select {
		case send <- held:
			held = ""
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
					if held != "" {
						dropped++
						held = ""
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
			stop = nil
			if !stopping {
				stopping = true
				skipped += len(run) - next
				if held != "" {
					skipped++
					held = ""
				}
			}
		}
	}
	h.logger.Info("collage: warmed pages", "warmed", warmed, "skipped", skipped, "failed", failed,
		"run", len(run), "duration", time.Since(began))
}

// warmWorker warms the keys handed to it until jobs is closed.
func (h *Handler) warmWorker(w *warmer, jobs <-chan string, results chan<- warmResult) {
	defer w.done.Done()
	for key := range jobs {
		results <- h.warmOne(w.ctx, key)
	}
}

// warmOne warms key and says how the run is to count it. A panic anywhere in it —
// serveWarm guards the serve, but not the store check after it or the metric —
// is a failed warm, never a worker lost with its result.
func (h *Handler) warmOne(ctx context.Context, key string) (result warmResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = warmResult{outcome: warmFail, err: fmt.Errorf("%w: %w", ErrPanic,
				&render.PanicError{Value: recovered, Stack: debug.Stack()})}
		}
	}()
	ok, err := h.warmKey(ctx, key)
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

// take takes key out of the queue, so the background run passes it over, and
// reports whether the warmer is still running: after StopWarming nothing is
// warmed, not even an action's redirect target.
func (w *warmer) take(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.pending, key)
	return !w.stopped
}

// warmRedirectTarget warms the page an action's response redirects to, before the
// response goes out, so the reader who submitted the form lands on a fresh HIT
// with what they just changed rather than waiting on its render, or being handed a
// stale copy when the background run has not got to it. It is called after the
// action's invalidation, which has queued the same keys for the background run.
//
// Every key the target's path was written under is warmed — one per variant, as
// the background run would — one at a time, each taken out of the queue just
// before, so it is not rendered twice and, should a render fail, the rest stay
// queued for the background run. Taking a key the background run has claimed
// already is harmless: the two renders coalesce in warmFlight, or one finds the
// other's entry fresh.
//
// The renders run under the warmer's context, not the request's: detached from the
// client going away, and carrying none of the values an auth middleware or the
// action left on the request's, which must not reach a shared entry. What bounds
// the wait is each fragment's own data timeout. A failure stops here and is logged
// at debug only: the page stays stale, the background run reports the upstream,
// and the redirect goes out all the same.
func (h *Handler) warmRedirectTarget(r *http.Request, action, location string) {
	w := h.warm
	if w == nil {
		return
	}
	path, ok := sameOriginPath(r, location)
	if !ok {
		return
	}
	keys := h.recipes.forPath(r.Host, path)
	if len(keys) > w.limit {
		keys = keys[:w.limit]
	}
	for _, key := range keys {
		if !w.take(key) {
			return
		}
		result := h.warmOne(w.ctx, key)
		if result.outcome == warmSkip && w.ctx.Err() == nil &&
			(errors.Is(result.err, errWarmNotStored) || errors.Is(result.err, errWarmShared)) {
			// It joined a render that began before this action's invalidation,
			// whose write the invalidation then refused, or that failed for the
			// warm leading it. That flight is over, so this one leads a render
			// of its own; without it the key, out of the queue, stays stale.
			result = h.warmOne(w.ctx, key)
		}
		switch result.outcome {
		case warmFail:
			h.logger.Debug("collage: an action's redirect target was not warmed, it stays stale",
				"action", action, "path", path, "err", result.err)
			return
		case warmSkip:
			if result.err != nil {
				h.logger.Debug("collage: an action's redirect target was skipped by warming",
					"action", action, "path", path, "err", result.err)
			}
		}
	}
}

// sameOriginPath returns the path location names on r's own origin: a relative
// reference resolved against r's URL, or an absolute http or https URL naming r's
// host. Anything else — another host, another scheme, an unparsable location — is
// no page of this cache, and ok is false. The query and fragment are dropped: a
// recipe is found by its path, every variant of it.
func sameOriginPath(r *http.Request, location string) (path string, ok bool) {
	loc, err := url.Parse(location)
	if err != nil || loc.Opaque != "" {
		return "", false
	}
	if loc.Scheme != "" || loc.Host != "" {
		// url.Parse lowercases the scheme; a host compares without case, as the
		// cache key's does. A scheme with no host ("http:/p") is left alone:
		// what a browser makes of it is not worth guessing at.
		if loc.Scheme != "" && loc.Scheme != "http" && loc.Scheme != "https" {
			return "", false
		}
		if loc.Host == "" || ascii.LowerString(loc.Host) != ascii.LowerString(r.Host) {
			return "", false
		}
	}
	path = r.URL.ResolveReference(loc).Path
	if path == "" {
		path = "/"
	}
	return path, true
}
