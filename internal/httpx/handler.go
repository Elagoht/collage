// Package httpx serves rendered pages over HTTP: it wires the router, the render
// engine, the cache, the dependency tracker, the plugin registry, and observability
// into one request lifecycle. The package is named httpx rather than http so that
// importing it never shadows the standard net/http package at a call site.
package httpx

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Elagoht/collage/internal/asset"
	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/dependency"
	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/types"
)

// ErrMissingDependency is returned by New when a required Deps field is nil. It is
// a single sentinel for every required field rather than one per field: a caller
// cannot react differently to a missing router than to a missing renderer, and the
// wrapped message names the field that was left unset.
var ErrMissingDependency = errors.New("collage: missing handler dependency")

// ErrNoRoute is the error reported to plugins through ErrorHook when no route
// matched the request. It exists because ErrorEvent.Err must name the failure being
// reported, and a not-found result arrives from the router as a normal match, not as
// an error.
//
// It is deliberately NOT named ErrNotFound: types.ErrNotFound means "the content
// this route resolves to does not exist", reported by a data handler and wrapped
// deep inside a render error. Both produce a 404, and a plugin asking why needs to
// tell them apart — an unmatched URL is a routing or link problem, a missing record
// is a content one.
var ErrNoRoute = errors.New("collage: no route matched the request")

// ErrEmptyErrorPage is the error reported to plugins when a registered error page
// renders successfully but produces no markup. It is a distinct failure from a
// render error — nothing failed, there is simply nothing to serve — and the handler
// falls through to the built-in page either way.
var ErrEmptyErrorPage = errors.New("collage: error page rendered empty")

// ErrPanic is the error reported to plugins through ErrorHook, under the stage
// "panic", when a collaborator panicked while serving a request and the handler
// recovered it into a 500. It is deliberately distinct from every other failure: a
// panic is a bug in the code that raised it, not a condition the request ran into,
// and an operator triaging one needs to tell it apart at a glance. The wrapped
// message carries the panic value and the stack it was raised on.
//
// A panic inside a data handler or a template function is not this: the render
// engine recovers those itself, as render.PanicError, and they follow the ordinary
// fragment failure policy.
var ErrPanic = errors.New("collage: panic recovered while serving the request")

// ErrAssetFailed is the error reported to plugins through ErrorHook when a mounted
// asset request completes with a status of 400 or above: a missing file, a
// disallowed method, or any other 4xx or 5xx asset.Mount itself decided to write.
// It is deliberately one sentinel for every such status rather than a family of
// them — a plugin reacting to "this asset request failed" needs no finer
// distinction than that, whether the status was 404 or 405, matching this
// project's one-sentinel-per-failure-mode rule — and the wrapped message names the
// status that was actually written, since the mount, not this package, chose it.
//
// A panic recovered while serving a mount is reported as ErrPanic instead, through
// the same path any other collaborator's panic takes: this sentinel covers only a
// mount that returned normally with an error status already on the wire.
var ErrAssetFailed = errors.New("collage: asset request failed")

// Pipeline stages, as reported to plugins through ErrorEvent.Stage. The field is
// documented as caller-defined rather than an enum, so these are the names this
// handler happens to use.
const (
	stageRoute        = "route"
	stageNotFound     = "not_found"
	stagePageResolved = "page_resolved"
	stageBeforeRender = "before_render"
	stageRender       = "render"
	stageAfterRender  = "after_render"
	stageCacheWrite   = "cache_write"
	stageErrorPage    = "error_page"
	stagePanic        = "panic"
	stageAsset        = "asset"
)

// contentTypeHTML is the Content-Type every rendered page and built-in error page
// is served with.
const contentTypeHTML = "text/html; charset=utf-8"

// renderTimeHeader carries the wall-clock duration of a fresh render. It is set in
// dev mode only, so production responses never advertise how long a page took to
// build.
const renderTimeHeader = "X-Collage-Render-Time"

// staticCacheTTL is the TTL a StrategyStatic page's cache entry is written with when
// the page sets no CacheTTL of its own: a hundred years, which is "until explicitly
// invalidated" as closely as the Cache interface can say it.
//
// It is not zero because zero already means something else. Cache.Set documents a
// ttl of zero or less as "use the cache's own configured default", and an
// application's own Cache implementation is entitled to read it that way — so
// passing zero would hand a static page whatever default that cache happens to
// carry, which is exactly the bug this constant exists to fix. A century is longer
// than any process this will run in and still an ordinary time.Duration, so no
// implementation has to special-case it.
const staticCacheTTL = 100 * 365 * 24 * time.Hour

// Deps are the collaborators a Handler needs. Router, Renderer, Tracker, and Logger
// are required; Cache, Metrics, Tracer, and Plugins may be left nil.
type Deps struct {
	// Router resolves a request to a page, a redirect, or a not-found result, and
	// holds the site's global not-found and error pages. Required.
	Router router.Router
	// Renderer composes a page's fragment tree into HTML. Required.
	Renderer render.Engine
	// Cache stores rendered pages. A nil Cache disables caching entirely: every
	// request renders, and nothing is ever stored or served from cache.
	Cache cache.Cache
	// Tracker records which cache keys were produced from which dependency tags.
	// Required, because it is the authority tag invalidation resolves through
	// even when the cache indexes the tags itself.
	Tracker dependency.Tracker
	// Plugins receives the lifecycle hooks. A nil *plugin.Registry is safe and
	// inert: it dispatches to no plugin.
	Plugins *plugin.Registry
	// Metrics receives per-response timings and cache events. Nil means no-op.
	Metrics observability.Metrics
	// Tracer starts one span per request. Nil means no-op.
	Tracer observability.Tracer
	// Logger receives failures the response itself cannot carry, such as an error
	// page that failed to render. Required.
	Logger *slog.Logger
	// DevMode serves diagnostic detail — the failing fragment and the full error
	// chain — on the built-in error page, and adds the render-time header. It
	// must be false in production: those diagnostics can carry credentials,
	// internal hostnames, and filesystem paths.
	DevMode bool
	// DefaultTTL is the cache TTL used for a page that sets no CacheTTL of its
	// own. Zero or less defers to the cache's own default TTL.
	DefaultTTL time.Duration
	// Vary lists the request headers a rendered page's content depends on. They
	// are joined into a Vary header on every publicly cacheable response.
	//
	// The application layer populates it from the router's enabled locale
	// sources: "Accept-Language" when header-locale resolution is on, "Cookie"
	// when cookie-locale resolution is. This framework's own cache key already
	// carries the resolved locale, so its cache was never at risk — but a shared
	// cache between the handler and the client, a CDN or a corporate proxy, keys
	// on the URL alone, and a locale negotiated from a header or a cookie is not
	// in the URL. Without this, such a cache hands one visitor's language to the
	// next.
	Vary []string
	// Mounts serves asset file systems under their own URL prefixes, checked
	// before every request is routed. A nil or empty Mounts serves no assets. See
	// Handler.serve for why checking them first is safe.
	Mounts []*asset.Mount
}

// Handler serves rendered pages over HTTP. It holds no per-request state, so one
// Handler is safe for concurrent use by as many requests as the server accepts.
type Handler struct {
	router     router.Router
	renderer   render.Engine
	cache      cache.Cache
	tracker    dependency.Tracker
	plugins    *plugin.Registry
	metrics    observability.Metrics
	tracer     observability.Tracer
	logger     *slog.Logger
	devMode    bool
	defaultTTL time.Duration
	vary       string
	mounts     []*asset.Mount
}

var _ http.Handler = (*Handler)(nil)

// New returns a Handler serving through d. It returns ErrMissingDependency, wrapped
// with the field's name, when Router, Renderer, Tracker, or Logger is nil. A nil
// Cache, Metrics, Tracer, or Plugins is accepted and made inert.
func New(d Deps) (*Handler, error) {
	if d.Router == nil {
		return nil, fmt.Errorf("%w: Router", ErrMissingDependency)
	}
	if d.Renderer == nil {
		return nil, fmt.Errorf("%w: Renderer", ErrMissingDependency)
	}
	if d.Tracker == nil {
		return nil, fmt.Errorf("%w: Tracker", ErrMissingDependency)
	}
	if d.Logger == nil {
		return nil, fmt.Errorf("%w: Logger", ErrMissingDependency)
	}
	return &Handler{
		router:     d.Router,
		renderer:   d.Renderer,
		cache:      d.Cache,
		tracker:    d.Tracker,
		plugins:    d.Plugins,
		metrics:    observability.MetricsOrNoop(d.Metrics),
		tracer:     observability.TracerOrNoop(d.Tracer),
		logger:     d.Logger,
		devMode:    d.DevMode,
		defaultTTL: d.DefaultTTL,
		// Joined once at construction: it is the same string on every response,
		// and it is copied out of d rather than aliased so a caller mutating its
		// slice afterwards cannot change what is served.
		vary: strings.Join(d.Vary, ", "),
		// Cloned for the same reason: a caller mutating d.Mounts after New returns
		// must not change what this Handler serves.
		mounts: slices.Clone(d.Mounts),
	}, nil
}

// ServeHTTP implements http.Handler: it runs the request lifecycle inside one span
// and reports the completed response to Metrics.
//
// A mount's URL space is claimed before routing, inside serve, but that check no
// longer opts a mounted request out of everything below: see serve and serveMount
// for why a mount now runs through the same timing, span, panic guard, and metric
// every page and document request gets.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	ctx, span := h.tracer.StartSpan(r.Context(), "collage.http")
	defer span.End()
	span.SetAttribute("http.method", r.Method)
	span.SetAttribute("http.path", r.URL.Path)
	r = r.WithContext(ctx)

	status := h.serveGuarded(w, r)

	span.SetAttribute("http.status_code", strconv.Itoa(status))
	h.metrics.HTTPResponse(ctx, status, r.URL.Path, time.Since(start))
}

// serveGuarded runs serve and turns a panic escaping it into a 500 on the normal
// error path — logged, dispatched to every ErrorHook, and counted by the metric
// ServeHTTP reports — instead of letting it unwind into net/http, which closes the
// connection with no status line at all.
//
// The render engine already recovers a panic inside a data handler or a template
// function, and turns it into an ordinary fragment failure. This covers everywhere
// else a request touches code the framework did not write: a Router of the
// application's own, a Cache implementation, a Metrics or Tracer implementation, and
// a plugin hook.
//
// A panic raised after the response headers are already on the wire — from inside
// the body write — cannot be turned into a 500 any more; serveFailure will try, and
// net/http will log the superfluous WriteHeader. That is still better than dropping
// the connection, and there is nothing else left to do at that point.
func (h *Handler) serveGuarded(w http.ResponseWriter, r *http.Request) (status int) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		status = h.serveFailure(w, r, failure{
			status: http.StatusInternalServerError,
			err:    fmt.Errorf("%w: %v\n%s", ErrPanic, recovered, debug.Stack()),
			stage:  stagePanic,
		})
	}()
	return h.serve(w, r)
}

// serve runs the request lifecycle and returns the status code it wrote.
//
// A mount is checked before routing, exactly as before, but no longer returns
// straight to net/http: internal/core's checkMountsDoNotShadow refuses to build a
// handler at all if any mount prefix would shadow a registered page or document
// path, so by the time a Handler exists every mount prefix is guaranteed to own
// URL space no route answers to. That guarantee is what makes checking mounts
// first — rather than falling through to the router and only trying a mount on a
// miss — safe: it can never take a request away from a page or a document, it
// costs nothing but a linear scan of however many mounts exist, and it is why a
// request for the bare mount prefix gets the mount's own plain-text 404 (see
// asset.Mount.Handles) instead of the router's HTML one. Without the close-out
// check enforcing that guarantee elsewhere, checking mounts before routing would
// be a correctness hazard rather than a safe optimization.
//
// What changed is that serve, not ServeHTTP, is where the check happens now:
// serve runs inside serveGuarded, which is what turns a panic into a 500 on the
// normal error path, and ServeHTTP is what times the request and reports the
// HTTPResponse metric. A mount request goes through serveMount so it gets all of
// that instead of bypassing it.
func (h *Handler) serve(w http.ResponseWriter, r *http.Request) int {
	for _, mount := range h.mounts {
		if mount.Handles(r.URL.Path) {
			return h.serveMount(w, r, mount)
		}
	}

	ctx := r.Context()

	match, err := h.router.Match(r)
	if err != nil {
		return h.serveFailure(w, r, failure{
			status: http.StatusInternalServerError,
			err:    fmt.Errorf("collage: match %q: %w", r.URL.Path, err),
			stage:  stageRoute,
		})
	}

	// Router is an interface the embedding application may implement itself, and a
	// nil result with a nil error is a contract violation that would otherwise
	// panic one request into a broken response rather than a 500.
	if match == nil {
		return h.serveFailure(w, r, failure{
			status: http.StatusInternalServerError,
			err:    fmt.Errorf("collage: match %q: router returned no result", r.URL.Path),
			stage:  stageRoute,
		})
	}

	if match.RedirectTo != "" {
		// Written by hand rather than through http.Redirect, which would add a
		// hyperlink body and a Content-Type for a GET: a redirect carries its
		// destination in the Location header, and a body only makes GET and HEAD
		// behave differently for no benefit.
		status := match.RedirectStatus
		if status < 300 || status > 399 {
			// Same reason as the nil-result guard: a Router of the application's
			// own making can leave the status unset, and WriteHeader panics on a
			// status outside the valid range.
			status = http.StatusFound
		}
		w.Header().Set("Location", match.RedirectTo)
		w.WriteHeader(status)
		return status
	}

	// Checked before the not-found fallback below, deliberately: on a successful
	// document match, Page is nil by MatchResult's own contract (exactly one of
	// Page and Document is set), and the nil-Page check below would otherwise
	// treat every matched document as a 404 before this branch ever ran.
	if match.Document != nil {
		return h.serveDocument(w, r, match)
	}

	// A nil Page is treated as not-found even when IsNotFound is false: Router is
	// an interface the embedding application may implement itself, and there is
	// nothing to render either way.
	if match.IsNotFound || match.Page == nil {
		return h.serveFailure(w, r, failure{
			status: http.StatusNotFound,
			err:    fmt.Errorf("%w: %q", ErrNoRoute, r.URL.Path),
			page:   match.Page,
			locale: match.Locale,
			stage:  stageNotFound,
		})
	}

	page := match.Page

	if err := h.plugins.PageResolved(ctx, &plugin.PageResolvedEvent{
		Page:   page,
		Locale: match.Locale,
		Path:   r.URL.Path,
	}); err != nil {
		return h.serveFailure(w, r, failure{
			status: http.StatusInternalServerError,
			err:    err,
			page:   page,
			locale: match.Locale,
			stage:  stagePageResolved,
		})
	}

	// Only GET and HEAD may be served from cache, and only a cacheable strategy
	// is looked up at all: an unsafe method's response is never a cached page.
	key := ""
	cacheable := h.cache != nil && page.Strategy.Cacheable() && (r.Method == http.MethodGet || r.Method == http.MethodHead)
	if cacheable {
		// The raw query is a cache dimension, not decoration: a fragment's data
		// handler receives the whole *http.Request and may legitimately render
		// from r.URL.Query(), so two queries against one path are two
		// representations. This does fragment the cache across utm_* and other
		// tracking variants of the same page, and it varies on parameter order
		// because the query is not canonicalized — correctness over hit rate. A
		// per-page allowlist of significant query parameters would recover both
		// and is the obvious future enhancement; it is deliberately not built
		// here, since guessing which parameters matter is the application's call.
		key = cache.Key(cache.KeyInput{
			Path:   r.URL.Path,
			Locale: match.Locale,
			Params: match.PathParams,
			Vary:   []string{r.URL.RawQuery},
		})
		lookupStart := time.Now()
		if content, etag, found := h.cache.Get(ctx, key); found {
			h.metrics.CacheEvent(ctx, observability.CacheHit, key)
			// The only place cacheHit is ever reported true: what it times is the
			// lookup that stood in for a render, not a render that did not happen.
			h.metrics.RenderDuration(ctx, page.Name, time.Since(lookupStart), true)
			return h.serveCached(w, r, page, content, etag)
		}
		h.metrics.CacheEvent(ctx, observability.CacheMiss, key)
	}

	// Deliberately after the cache lookup: BeforeRenderHook documents that it
	// does not fire when a cached render is served instead of a fresh one, which
	// is what distinguishes it from PageResolvedHook.
	if err := h.plugins.BeforeRender(ctx, &plugin.BeforeRenderEvent{
		Page:   page,
		Locale: match.Locale,
		Path:   r.URL.Path,
	}); err != nil {
		return h.serveFailure(w, r, failure{
			status: http.StatusInternalServerError,
			err:    err,
			page:   page,
			locale: match.Locale,
			stage:  stageBeforeRender,
		})
	}

	renderStart := time.Now()
	result, err := h.renderer.Render(ctx, types.NewRenderContext(ctx, r, page, match.Locale, match.PathParams))
	renderTime := time.Since(renderStart)
	if err != nil {
		// result is non-nil on every Render path, including a fatal error, so
		// NotFound can be read here safely; the error is checked first, as
		// Render's contract requires.
		status := http.StatusInternalServerError
		if result.NotFound {
			// A required fragment's data handler reported that the content
			// itself does not exist, as distinct from a failure to fetch it: the
			// request is a 404, not a 500, and resolves to page's own
			// NotFoundPage through errorPageFor exactly as a router miss does.
			status = http.StatusNotFound
		}
		return h.serveFailure(w, r, failure{
			status:   status,
			err:      err,
			page:     page,
			locale:   match.Locale,
			fragment: failedFragment(result),
			stage:    stageRender,
		})
	}

	// The hook may replace the HTML, and the replacement is what goes on the wire
	// and, below, into the cache.
	afterRender := &plugin.AfterRenderEvent{
		Page:     page,
		Locale:   match.Locale,
		Degraded: result.Degraded(),
		HTML:     result.HTML,
	}
	if err := h.plugins.AfterRender(ctx, afterRender); err != nil {
		// No fragment is named: the failure is the plugin's, and pointing the dev
		// page at a fragment that merely happened to be degraded would send a
		// developer looking in the wrong place.
		return h.serveFailure(w, r, failure{
			status: http.StatusInternalServerError,
			err:    err,
			page:   page,
			locale: match.Locale,
			stage:  stageAfterRender,
		})
	}
	content := afterRender.HTML

	// A degraded render is complete enough to serve but must never be cached:
	// caching it would pin one request's transient fragment failure in front of
	// every later request. A HEAD is served from cache but never populates it —
	// it produced no body to store.
	etag := ""
	if cacheable && r.Method == http.MethodGet && !result.Degraded() {
		etag = h.writeCache(r, key, page, content, result.DependencyTags)
	}
	if etag == "" {
		etag = cache.ETag(content)
	}

	header := w.Header()
	header.Set("Content-Type", contentTypeHTML)
	header.Set("ETag", etag)
	h.setCacheHeaders(header, page.Strategy, page.CacheTTL)
	if h.devMode {
		header.Set(renderTimeHeader, renderTime.String())
	}
	w.WriteHeader(http.StatusOK)
	writeBody(w, content)

	return http.StatusOK
}

// serveCached writes a cache hit: a 304 when the request's If-None-Match matches
// the stored ETag, otherwise a 200 carrying the stored content. It returns the
// status it wrote.
func (h *Handler) serveCached(w http.ResponseWriter, r *http.Request, page *types.Page, content []byte, etag string) int {
	header := w.Header()
	header.Set("ETag", etag)
	h.setCacheHeaders(header, page.Strategy, page.CacheTTL)

	if cache.ETagMatch(r.Header.Get("If-None-Match"), etag) {
		// No Content-Type and no body: a 304 tells the client its copy is still
		// good, it does not re-describe the representation.
		w.WriteHeader(http.StatusNotModified)
		return http.StatusNotModified
	}

	header.Set("Content-Type", contentTypeHTML)
	w.WriteHeader(http.StatusOK)
	writeBody(w, content)

	return http.StatusOK
}

// writeCache stores content under key, after giving CacheWriteHook the chance to
// adjust the TTL and tags or to suppress the write, and records the key's tags with
// the tracker. Both write paths track: SetTagged indexes the tags inside the cache
// and Track indexes them in the tracker, and invalidation may go through either, so
// the two indexes are kept deliberately redundant.
//
// A failure here never fails the request. The page has already rendered, and
// serving it uncached is strictly better than turning a cache problem into a 500.
//
// It returns the ETag the cache stored the entry under, or the empty string when
// nothing was written. Cache is an interface the application may implement, and its
// contract is that Set returns the content's ETag — so the stored value, not a
// locally recomputed one, is what the fresh response must advertise. A cache that
// derives ETags its own way would otherwise send one value on the fresh response and
// a different one on every hit that followed, and every conditional request would
// miss.
func (h *Handler) writeCache(r *http.Request, key string, page *types.Page, content []byte, tags []string) string {
	ctx := r.Context()

	event := &plugin.CacheWriteEvent{
		Key:  key,
		Page: page,
		TTL:  h.ttlFor(page.Strategy, page.CacheTTL),
		Tags: tags,
	}
	if err := h.plugins.CacheWrite(ctx, event); err != nil {
		h.reportError(r, failure{err: err, page: page, stage: stageCacheWrite})
		return ""
	}
	if event.Skip {
		return ""
	}

	var etag string
	var err error
	if tagged, ok := h.cache.(cache.TaggedCache); ok {
		etag, err = tagged.SetTagged(ctx, key, content, event.TTL, event.Tags)
	} else {
		etag, err = h.cache.Set(ctx, key, content, event.TTL)
	}
	if err != nil {
		h.reportError(r, failure{err: fmt.Errorf("collage: cache write: %w", err), page: page, stage: stageCacheWrite})
		return ""
	}
	h.metrics.CacheEvent(ctx, observability.CacheSet, key)

	if err := h.tracker.Track(ctx, key, event.Tags); err != nil {
		h.reportError(r, failure{err: fmt.Errorf("collage: track %q: %w", key, err), page: page, stage: stageCacheWrite})
	}
	return etag
}

// ttlFor returns the cache TTL for a route with the given strategy and cacheTTL:
// cacheTTL itself when set, then staticCacheTTL for StrategyStatic, otherwise the
// handler's DefaultTTL, which may itself be zero to defer to the cache's default.
//
// It is shared by the page and document paths. They are parameterized on strategy
// and cacheTTL directly, rather than on *types.Page or *types.Document, because
// the two types share no common field accessor to feed a single implementation
// through, and this package uses neither an interface for that nor reflection —
// the values themselves are all the logic below needs.
//
// The StrategyStatic case is what makes that strategy mean what it says. "Render
// once and serve until explicitly invalidated" is its documented contract, but
// Static() sets no CacheTTL, so without this a static route fell through to
// DefaultTTL — which pkg/collage's own defaulting forces to five minutes — and every
// static route silently re-rendered on that cycle.
func (h *Handler) ttlFor(strategy types.RenderStrategy, cacheTTL time.Duration) time.Duration {
	if cacheTTL > 0 {
		return cacheTTL
	}
	if strategy == types.StrategyStatic {
		return staticCacheTTL
	}
	return h.defaultTTL
}

// setCacheHeaders writes the Cache-Control for a route with the given strategy and
// cacheTTL, and, whenever that response is publicly cacheable, the Vary header
// built from Deps.Vary. Vary belongs only on a public response: it tells a shared
// cache which request headers select between representations, and a no-store
// response has no representation to select. Shared by the page and document paths;
// see ttlFor for why it is parameterized rather than typed on either route kind.
func (h *Handler) setCacheHeaders(header http.Header, strategy types.RenderStrategy, cacheTTL time.Duration) {
	control := h.cacheControl(strategy, cacheTTL)
	header.Set("Cache-Control", control)

	if h.vary == "" || !strings.HasPrefix(control, "public") {
		return
	}
	header.Set("Vary", h.vary)
}

// cacheControl returns the Cache-Control value for a route with the given render
// strategy: static is cached but always revalidated, incremental is cached for its
// effective TTL, and dynamic is never stored. The incremental value is derived from
// cacheTTL, not from a TTL a CacheWriteHook adjusted, so the header a client sees
// does not change from request to request. Shared by the page and document paths;
// see ttlFor for why it is parameterized rather than typed on either route kind.
func (h *Handler) cacheControl(strategy types.RenderStrategy, cacheTTL time.Duration) string {
	switch strategy {
	case types.StrategyStatic:
		return "public, max-age=0, must-revalidate"
	case types.StrategyIncremental:
		seconds := int64(h.ttlFor(strategy, cacheTTL) / time.Second)
		if seconds < 0 {
			seconds = 0
		}
		return "public, max-age=" + strconv.FormatInt(seconds, 10)
	default:
		return "no-store"
	}
}

// writeBody writes content, including for a HEAD request.
//
// Writing on a HEAD looks wrong and is not: net/http discards a HEAD response's body
// itself, and it derives Content-Length from what the handler wrote. Returning early
// instead — which is what this did — left every HEAD response advertising
// "Content-Length: 0" for a resource whose GET reports its real size, which is
// exactly what HEAD exists to tell a client. The body is built either way, since the
// page has already rendered; what is skipped is only the copy onto the socket, and
// net/http is the one positioned to skip it.
func writeBody(w http.ResponseWriter, content []byte) {
	if len(content) == 0 {
		return
	}
	// The error is deliberately unchecked: the status line and headers are
	// already on the wire, so there is nothing left to tell the client, and a
	// client that hung up mid-body is not a server fault worth logging.
	_, _ = w.Write(content)
}
