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
	"strconv"
	"time"

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

// ErrNotFound is the error reported to plugins through ErrorHook when a request
// resolves to no content. It exists because ErrorEvent.Err must name the failure
// being reported, and a not-found result arrives from the router as a normal match,
// not as an error.
var ErrNotFound = errors.New("collage: no route matched the request")

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
)

// contentTypeHTML is the Content-Type every rendered page and built-in error page
// is served with.
const contentTypeHTML = "text/html; charset=utf-8"

// renderTimeHeader carries the wall-clock duration of a fresh render. It is set in
// dev mode only, so production responses never advertise how long a page took to
// build.
const renderTimeHeader = "X-Collage-Render-Time"

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
	}, nil
}

// ServeHTTP implements http.Handler: it runs the request lifecycle inside one span
// and reports the completed response to Metrics.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	ctx, span := h.tracer.StartSpan(r.Context(), "collage.http")
	defer span.End()
	span.SetAttribute("http.method", r.Method)
	span.SetAttribute("http.path", r.URL.Path)
	r = r.WithContext(ctx)

	status := h.serve(w, r)

	span.SetAttribute("http.status_code", strconv.Itoa(status))
	h.metrics.HTTPResponse(ctx, status, r.URL.Path, time.Since(start))
}

// serve runs the request lifecycle and returns the status code it wrote.
func (h *Handler) serve(w http.ResponseWriter, r *http.Request) int {
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

	// A nil Page is treated as not-found even when IsNotFound is false: Router is
	// an interface the embedding application may implement itself, and there is
	// nothing to render either way.
	if match.IsNotFound || match.Page == nil {
		return h.serveFailure(w, r, failure{
			status: http.StatusNotFound,
			err:    fmt.Errorf("%w: %q", ErrNotFound, r.URL.Path),
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
		key = cache.Key(cache.KeyInput{Path: r.URL.Path, Locale: match.Locale, Params: match.PathParams})
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
		return h.serveFailure(w, r, failure{
			status:   http.StatusInternalServerError,
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
	if cacheable && r.Method == http.MethodGet && !result.Degraded() {
		h.writeCache(r, key, page, content, result.DependencyTags)
	}

	header := w.Header()
	header.Set("Content-Type", contentTypeHTML)
	header.Set("ETag", cache.ETag(content))
	header.Set("Cache-Control", h.cacheControl(page))
	if h.devMode {
		header.Set(renderTimeHeader, renderTime.String())
	}
	w.WriteHeader(http.StatusOK)
	writeBody(w, r, content)

	return http.StatusOK
}

// serveCached writes a cache hit: a 304 when the request's If-None-Match matches
// the stored ETag, otherwise a 200 carrying the stored content. It returns the
// status it wrote.
func (h *Handler) serveCached(w http.ResponseWriter, r *http.Request, page *types.Page, content []byte, etag string) int {
	header := w.Header()
	header.Set("ETag", etag)
	header.Set("Cache-Control", h.cacheControl(page))

	if cache.ETagMatch(r.Header.Get("If-None-Match"), etag) {
		// No Content-Type and no body: a 304 tells the client its copy is still
		// good, it does not re-describe the representation.
		w.WriteHeader(http.StatusNotModified)
		return http.StatusNotModified
	}

	header.Set("Content-Type", contentTypeHTML)
	w.WriteHeader(http.StatusOK)
	writeBody(w, r, content)

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
func (h *Handler) writeCache(r *http.Request, key string, page *types.Page, content []byte, tags []string) {
	ctx := r.Context()

	event := &plugin.CacheWriteEvent{
		Key:  key,
		Page: page,
		TTL:  h.ttlFor(page),
		Tags: tags,
	}
	if err := h.plugins.CacheWrite(ctx, event); err != nil {
		h.reportError(r, failure{err: err, page: page, stage: stageCacheWrite})
		return
	}
	if event.Skip {
		return
	}

	var err error
	if tagged, ok := h.cache.(cache.TaggedCache); ok {
		_, err = tagged.SetTagged(ctx, key, content, event.TTL, event.Tags)
	} else {
		_, err = h.cache.Set(ctx, key, content, event.TTL)
	}
	if err != nil {
		h.reportError(r, failure{err: fmt.Errorf("collage: cache write: %w", err), page: page, stage: stageCacheWrite})
		return
	}
	h.metrics.CacheEvent(ctx, observability.CacheSet, key)

	if err := h.tracker.Track(ctx, key, event.Tags); err != nil {
		h.reportError(r, failure{err: fmt.Errorf("collage: track %q: %w", key, err), page: page, stage: stageCacheWrite})
	}
}

// ttlFor returns the cache TTL for page: its own CacheTTL when set, otherwise the
// handler's DefaultTTL, which may itself be zero to defer to the cache's default.
func (h *Handler) ttlFor(page *types.Page) time.Duration {
	if page != nil && page.CacheTTL > 0 {
		return page.CacheTTL
	}
	return h.defaultTTL
}

// cacheControl returns the Cache-Control value for page's render strategy: a static
// page is cached but always revalidated, an incremental page is cached for its
// effective TTL, and a dynamic page is never stored. The incremental value is
// derived from the page, not from a TTL a CacheWriteHook adjusted, so the header a
// client sees does not change from request to request.
func (h *Handler) cacheControl(page *types.Page) string {
	switch page.Strategy {
	case types.StrategyStatic:
		return "public, max-age=0, must-revalidate"
	case types.StrategyIncremental:
		seconds := int64(h.ttlFor(page) / time.Second)
		if seconds < 0 {
			seconds = 0
		}
		return "public, max-age=" + strconv.FormatInt(seconds, 10)
	default:
		return "no-store"
	}
}

// writeBody writes content unless r is a HEAD request, which carries the headers of
// the response it would have received and no body at all.
func writeBody(w http.ResponseWriter, r *http.Request, content []byte) {
	if r.Method == http.MethodHead || len(content) == 0 {
		return
	}
	// The error is deliberately unchecked: the status line and headers are
	// already on the wire, so there is nothing left to tell the client, and a
	// client that hung up mid-body is not a server fault worth logging.
	_, _ = w.Write(content)
}
