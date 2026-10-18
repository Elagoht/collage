// Package httpx serves rendered pages over HTTP: it wires the router, the render
// engine, the cache, the dependency tracker, the plugin registry, and observability
// into one request lifecycle. The package is named httpx rather than http so that
// importing it never shadows the standard net/http package at a call site.
package httpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Elagoht/collage/internal/ascii"
	"github.com/Elagoht/collage/internal/asset"
	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/csrf"
	"github.com/Elagoht/collage/internal/dependency"
	"github.com/Elagoht/collage/internal/devhost"
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
// and an operator triaging one needs to tell it apart at a glance. It wraps a
// *render.PanicError, which holds the panic value and the stack it was raised on;
// the message carries the value, and the log line carries the stack.
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

// Pipeline stages, as reported to plugins through ErrorEvent.Stage: the names
// types defines once, so the public constants pkg/collage publishes for them are
// the strings this handler sends.
const (
	stageRoute        = types.StageRoute
	stageNotFound     = types.StageNotFound
	stageGuard        = types.StageGuard
	stageBeforeRender = types.StageBeforeRender
	stageBeforeAction = types.StageBeforeAction
	stageRender       = types.StageRender
	stageAfterRender  = types.StageAfterRender
	stageCacheWrite   = types.StageCacheWrite
	stageErrorPage    = types.StageErrorPage
	stagePanic        = types.StagePanic
	stageAsset        = types.StageAsset
	stageHandler      = types.StageHandler
	stagePlugin       = types.StagePlugin
)

// contentTypeHTML is the Content-Type every rendered page and built-in error page
// is served with.
const contentTypeHTML = "text/html; charset=utf-8"

// renderTimeHeader carries the wall-clock duration of a fresh render. It is set in
// dev mode only, so production responses never advertise how long a page took to
// build.
const renderTimeHeader = "X-Collage-Render-Time"

// CacheStatusHeader says where a cacheable page's or document's response came
// from: HIT, STALE or MISS. It is set only on a cacheable GET or HEAD that
// answered with the page, never on a failure, and never in development, where
// the cache is not read, so its absence is never mistaken for a miss.
const CacheStatusHeader = "X-Collage-Cache"

// The values of CacheStatusHeader.
const (
	// cacheHit: a fresh entry was served without rendering.
	cacheHit = "HIT"
	// cacheStale: the entry was past an invalidation or its expiry, the render
	// replacing it failed or was degraded, and the old entry was served instead.
	cacheStale = "STALE"
	// cacheMiss: the response is a render, from no entry or a stale one.
	cacheMiss = "MISS"
)

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
	// Origins resolves a host's public origin, for collage.BaseURL. Nil leaves
	// BaseURL "" in a render.
	Origins OriginFunc
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
	//
	// It also refuses, 403, a request whose Host does not name this machine:
	// see internal/devhost.
	DevMode bool
	// DevHosts are the names, besides localhost, IP addresses and the reserved
	// names (example.com, *.test, …: see internal/devhost), that the
	// development Host check allows: the host the server listens on and the
	// COLLAGE_DEV_HOST list, which under "collage dev" includes the host its
	// proxy listens on. Ignored outside development, and for a capture.
	DevHosts []string
	// Invalidations reports how many tag invalidations and purges have started
	// and how many have finished, the finished count read first. A render is
	// stored only when the two were equal as it began and the started count has
	// not moved by its write: otherwise it overlapped an invalidation, and may
	// have read what it replaced. Nil means no such rule.
	Invalidations func() (started, finished uint64)
	// DefaultTTL is the cache TTL used for a page that sets no CacheTTL of its
	// own. Zero or less defers to the cache's own default TTL.
	DefaultTTL time.Duration
	// Mounts serves asset file systems under their own URL prefixes, checked
	// before every request is routed. A nil or empty Mounts serves no assets. See
	// Handler.serve for why checking them first is safe.
	Mounts []*asset.Mount
	// Handlers are the application's own http.Handlers, each answering every
	// request under its prefix. They are checked after Mounts and before the
	// router, and like Mounts they are guaranteed by internal/core not to
	// overlap any route.
	Handlers []HandlerMount
	// DevSources are the file systems a development page reloads itself when
	// they change — the templates as read from disk — in addition to every
	// mount's. Ignored outside development.
	DevSources []fs.FS
	// PageReady reports whether a page can render: one with a layout only once
	// registration has bound its content into it. It is how an action answering
	// with a page built on the spot is told so, rather than served a blank one.
	// Nil skips the check.
	PageReady func(*types.Page) bool
	// Middleware wraps the handling of every request, first element outermost.
	// It runs inside the span, the metrics and the panic guard, and before a
	// mount, a handler or the router sees the request.
	Middleware []Middleware
	// MaxBodyBytes bounds an action's request body when the action declares no
	// bound of its own. Zero selects the framework's default; negative means
	// unbounded, which is a decision worth making deliberately.
	MaxBodyBytes int64
	// Invalidator drops cache entries by tag, and is what backs
	// ActionResult.InvalidateTags. Nil makes that field inert.
	Invalidator Invalidator
	// Routes builds page and action URLs, and is what backs rc.URL and
	// rc.ActionURL in an action's handler. The zero value leaves them reporting
	// that no routes are known.
	Routes types.Routes
	// CSRF verifies unsafe requests to actions and issues the tokens
	// {{csrfToken}} renders. Nil turns forgery checking off entirely, which is
	// what an application with no forms and no key gets.
	CSRF *csrf.Guard
	// FrameOptions is the X-Frame-Options value set on every response before
	// routing, so a mount, a handler, the router — and a plugin's middleware,
	// which can override it — all inherit it. Empty sends none.
	FrameOptions string
	// NoSniff sets X-Content-Type-Options: nosniff on every response before
	// routing when true, on the same terms as FrameOptions.
	NoSniff bool
	// TrustedProxies are the proxies whose X-Forwarded-For ClientIP believes.
	// Empty trusts none: the client is always RemoteAddr.
	TrustedProxies []netip.Prefix
	// MaxRecipes bounds the warm recipes kept, one per cache key written, the
	// oldest evicted first. Zero or less means 10000.
	MaxRecipes int
	// WarmLimit caps how many keys one Warm call re-renders, the most recently
	// written first. Zero means 1000; negative turns warming off, so Warm does
	// nothing. Warming is off as well in development and with no Cache.
	WarmLimit int
	// WriteTimeout is the server's, so an action holds its wait for its
	// redirect target to what is left of it. Zero means none.
	WriteTimeout time.Duration
}

// Handler serves rendered pages over HTTP. It holds no per-request state, so one
// Handler is safe for concurrent use by as many requests as the server accepts.
type Handler struct {
	origins    OriginFunc
	router     router.Router
	renderer   render.Engine
	cache      cache.Cache
	tracker    dependency.Tracker
	plugins    *plugin.Registry
	metrics    observability.Metrics
	tracer     observability.Tracer
	logger     *slog.Logger
	devMode    bool
	devHosts   []string
	defaultTTL time.Duration
	// invalidations is Deps.Invalidations, read around a render.
	invalidations func() (started, finished uint64)
	mounts        []*asset.Mount
	handlers      []HandlerMount
	chain         http.Handler
	reload        *reloadHub
	pageReady     func(*types.Page) bool
	maxBodyBytes  int64
	writeTimeout  time.Duration
	invalidator   Invalidator
	routes        types.Routes
	csrf          *csrf.Guard
	frameOptions  string
	noSniff       bool
	trusted       []netip.Prefix
	// flight coalesces concurrent renders of one cache key, so an expiring
	// popular page costs one render rather than one per request that arrives
	// while it is being re-made.
	flight *flight
	// recipes records, per cache key written, how to rebuild the request that
	// wrote it, so an invalidated key can be re-rendered before anyone asks.
	recipes *recipes
	// warmFlight coalesces warm renders, apart from flight: a visitor joining a
	// warm render would wait on the upstream it waits for, and a warm joining a
	// visitor's would take a render that may use stale data. See warmKey.
	warmFlight *flight
	// warm re-renders invalidated keys in the background. Nil when warming is
	// off: in development, with no cache, or with a negative WarmLimit.
	warm *warmer

	// bodyTimeoutWarning logs, once, that an action's body timeout could not
	// be set. See setBodyDeadlines.
	bodyTimeoutWarning sync.Once
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
	h := &Handler{
		origins:       d.Origins,
		router:        d.Router,
		renderer:      d.Renderer,
		cache:         d.Cache,
		tracker:       d.Tracker,
		plugins:       d.Plugins,
		metrics:       observability.MetricsOrNoop(d.Metrics),
		tracer:        observability.TracerOrNoop(d.Tracer),
		logger:        d.Logger,
		devMode:       d.DevMode,
		devHosts:      slices.Clone(d.DevHosts),
		defaultTTL:    d.DefaultTTL,
		invalidations: d.Invalidations,
		// Cloned so a caller mutating d.Mounts after New returns cannot change
		// what this Handler serves.
		mounts:       slices.Clone(d.Mounts),
		handlers:     slices.Clone(d.Handlers),
		pageReady:    d.PageReady,
		maxBodyBytes: d.MaxBodyBytes,
		writeTimeout: d.WriteTimeout,
		invalidator:  d.Invalidator,
		routes:       d.Routes,
		csrf:         d.CSRF,
		frameOptions: d.FrameOptions,
		noSniff:      d.NoSniff,
		trusted:      slices.Clone(d.TrustedProxies),
		flight:       newFlight(),
		recipes:      newRecipes(d.MaxRecipes),
		warmFlight:   newFlight(),
	}
	if !d.DevMode && d.Cache != nil && d.WarmLimit >= 0 {
		h.warm = newWarmer(d.WarmLimit)
	}
	// Composed once rather than per request: a middleware constructor is
	// allowed to do setup work, and doing it on every request would be a cost
	// nobody asked for.
	if len(d.Middleware) > 0 {
		h.chain = compose(slices.Clone(d.Middleware), http.HandlerFunc(h.serveChained))
	}
	if d.DevMode {
		sources := slices.Clone(d.DevSources)
		for _, mount := range d.Mounts {
			sources = append(sources, mount.FS())
		}
		h.reload = newReloadHub(sources)
	}
	return h, nil
}

// serveForeignHost refuses a development request for a Host that does not
// name this machine. Plain text that names the fix and nothing of the
// application: the reader is, as far as the server can tell, another site.
func serveForeignHost(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	// Quoted, never echoed: the Host is the requester's to choose.
	fmt.Fprintf(w, "collage: development mode answers only this machine's names; %q is not one. "+
		"To reach it by that name, add it to %s (a comma-separated list) or set Server.Host to it.\n",
		r.Host, devhost.EnvHost)
}

// ServeHTTP implements http.Handler: it runs the request lifecycle inside one span
// and reports the completed response to Metrics.
//
// A mount's URL space is claimed before routing, inside serve, but that check no
// longer opts a mounted request out of everything below: see serve and serveMount
// for why a mount now runs through the same timing, span, panic guard, and metric
// every page and document request gets.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The baseline security headers, set before anything writes a response —
	// before routing, so a mount, a handler and the router inherit them, and
	// before middleware, so a plugin such as elagoht/secure that sets its own
	// X-Frame-Options or Content-Security-Policy overrides them with a plain
	// Header().Set. They are the floor every response stands on, not the ceiling.
	h.setBaselineHeaders(w.Header())

	// Before anything a development server serves that a production one does
	// not — the reload stream, the worker, a stack on an error page — and
	// before middleware, which a development toolbar is. Before the request
	// hooks too, deliberately: a refused request reaches no plugin, not even
	// one that only logs — this is development only, and refusing before
	// anything of the application runs is the point.
	//
	// A capture is exempt: it is the application asking itself, in process,
	// with BaseURL's host — a production name a development build does not
	// answer — and its marker is a context value no request from the wire
	// can carry.
	if h.devMode && !types.IsCapture(r.Context()) && !devhost.Allowed(r.Host, h.devHosts...) {
		serveForeignHost(w, r)
		return
	}

	// Ahead of everything, middleware included: it is the development tool's
	// own channel, and an auth middleware refusing it would turn live reload off
	// with nothing to say why. Outside the metrics too — a stream that stays
	// open for an hour is not a slow request.
	if h.reload != nil && r.URL.Path == devReloadPath {
		h.reload.ServeHTTP(w, r)
		return
	}
	if h.reload != nil && r.URL.Path == devReloadWorkerPath {
		serveReloadWorker(w)
		return
	}
	// The proxies ClientIP believes, in the context from here on: everything
	// below — a plugin's RequestHook and middleware included — may ask who the
	// client is.
	if len(h.trusted) > 0 {
		r = r.WithContext(WithTrustedProxies(r.Context(), h.trusted))
	}
	// What the request resolves to is recorded here, in the context every later
	// frame shares, so a hook, a metric or a trace can name the route instead of
	// the raw path. See RouteInfo. Ahead of the request hooks: a hook keeps the
	// context it was handed and reads the route from it once the response is
	// written. A request answered before routing leaves it empty.
	route := &routeRef{start: time.Now()}
	r = r.WithContext(context.WithValue(r.Context(), routeCtxKey{}, route))
	if h.origins != nil {
		r = r.WithContext(WithOrigins(r.Context(), h.origins))
	}

	// Before the request's span: a plugin carrying a trace in from the caller
	// makes its span the parent of collage's own. See plugin.RequestHook. And
	// before the path checks below, so a request answered there — a scanner's
	// "/a/../etc/passwd", its encoded slash — is still one the hooks see: a
	// ban counting probes, a trace, a log line. The development Host refusal
	// above, and the reload channel, are the exceptions: development only, and
	// answered before anything of the application runs.
	r, finish := h.plugins.Request(r)

	// Before middleware and routing, so that a check on a path — "skip
	// /_collage/", "protect /admin/" — never meets "/_collage/../admin" and lets
	// it through to the page behind it.
	if cleaned, dirty := cleanPath(r.URL.Path); dirty {
		// A path made dirty by an encoded slash is not cleaned into another
		// one: "/blog/%2Fhello" is not "/blog/hello", "/a%2F..%2Fb" is not
		// "/b". An encoded slash reaches no route, and redirecting it to one
		// would make it a separator after all. It is answered as not found,
		// here, before a middleware reads the decoded path it spells.
		if strings.Contains(ascii.LowerString(r.URL.EscapedPath()), "%2f") {
			h.ServeStatus(w, r, http.StatusNotFound)
			finish(http.StatusNotFound)
			return
		}
		finish(redirectClean(w, r, cleaned))
		return
	}

	start := time.Now()

	ctx, span := h.startRequestSpan(r)
	r = withVarySet(r.WithContext(ctx))

	status := h.serveGuarded(w, r, route)

	span.SetAttribute("http.status_code", strconv.Itoa(status))
	span.End()
	h.metrics.HTTPResponse(ctx, status, r.URL.Path, time.Since(start))
	finish(status)
}

// setBaselineHeaders writes the framework's default security headers onto h,
// the response's header map, at the start of every request. A later
// Header().Set — by a plugin's middleware or by a handler — replaces either of
// them, so these are defaults, not decisions the application cannot change. An
// empty frameOptions, or noSniff being off, sends that header not at all.
func (h *Handler) setBaselineHeaders(header http.Header) {
	if h.noSniff {
		header.Set("X-Content-Type-Options", "nosniff")
	}
	if h.frameOptions != "" {
		header.Set("X-Frame-Options", h.frameOptions)
	}
}

// startRequestSpan opens the request's span, containing a panic from an
// application's Tracer instead of letting it reach net/http.
//
// This is the one part of the tracing bracket that runs before anything has been
// written, so it is the one part where a panic costs the client its response: the
// connection closes with no status line, and the caller sees what looks like a
// network failure rather than a bug in its tracer. A tracer that cannot start a
// span is a reason to serve the page without tracing, not a reason not to serve it.
//
// SetAttribute is inside the guard because it runs here too, before the response.
// When it panics the span is abandoned rather than ended: End would be a third call
// into a Tracer that has already demonstrated it panics, and the framework has
// nothing to gain by making it.
func (h *Handler) startRequestSpan(r *http.Request) (ctx context.Context, span observability.Span) {
	ctx, span = r.Context(), observability.NoopSpan{}

	defer func() {
		if rec := recover(); rec != nil {
			h.logger.Error("collage: tracer panicked starting the request span",
				"panic", rec, "path", r.URL.Path, "stack", string(debug.Stack()))
			ctx, span = r.Context(), observability.NoopSpan{}
		}
	}()

	ctx, span = h.tracer.StartSpan(r.Context(), "collage.http")
	span.SetAttribute("http.method", r.Method)
	span.SetAttribute("http.path", r.URL.Path)
	return ctx, span
}

// queryVary turns a request's query into the cache-key dimensions a route declares.
//
// A nil allow keeps the raw query whole, which is the default and the conservative
// reading: the framework cannot know which parameters a data handler consults, and
// merging two representations serves one visitor another's page. The cost is that
// every "?utm_source=..." variant is its own entry, so a crawler can evict a bounded
// cache without ever asking for a distinct page — which is why a route can say
// otherwise.
//
// A non-nil allow — including an empty one, which drops the query entirely — selects
// only the named parameters, and canonicalises what survives. Canonicalising is safe
// precisely here and not above: once the route has said which parameters matter,
// their order and the absence of everything else are no longer facts about the
// representation. Values are kept in their given order within a parameter, because
// repeating a parameter is how a request expresses a list.
func queryVary(u *url.URL, allow []string) []string {
	if allow == nil {
		return []string{u.RawQuery}
	}
	if len(allow) == 0 || u.RawQuery == "" {
		return nil
	}

	values := u.Query()
	selected := make(url.Values, len(allow))
	for _, name := range allow {
		if vs, ok := values[name]; ok {
			selected[name] = vs
		}
	}
	if len(selected) == 0 {
		return nil
	}
	// url.Values.Encode sorts by key, which is the canonicalisation.
	return []string{selected.Encode()}
}

// serveGuarded runs serve and turns a panic escaping it into a 500 on the normal
// error path — logged, dispatched to every ErrorHook, and counted by the metric
// ServeHTTP reports — instead of letting it unwind into net/http, which closes the
// connection with no status line at all.
//
// The render engine already recovers a panic inside a data handler, a template
// function, or a document handler, and turns it into an ordinary failure. This
// covers everywhere else *inside serve* a request touches code the framework did
// not write: a Router of the application's own, a Cache implementation, a
// dependency Tracker, a Metrics implementation called from serve, an asset mount's
// fs.FS, and a plugin hook.
//
// It does not cover the span-closing and metric calls in ServeHTTP itself —
// SetAttribute, End, and HTTPResponse after the response — and a panic in any of
// them still unwinds into net/http. That is deliberate rather than overlooked:
// those calls bracket the response instead of producing it, so by the time they run
// there is no status left to write and nothing a 500 could add, and moving them
// inside would mean reporting a failed span through the very Tracer that just
// panicked. The comment used to claim it covered all of them, which is the kind of
// promise a panic guard must not make loosely.
//
// The tracer calls that run *before* the response are a different case and are
// guarded, by startRequestSpan rather than here: a panic there costs the client its
// response entirely.
//
// The response's content type follows what the request resolved to, not what this
// frame knows: serve records the resolved route in route, so a panic in an
// application's Cache on a document route produces the document's plain text and a
// panic on a page route produces the HTML error page. See routeKind — that record
// is the whole reason this frame does not have to be told what it is serving.
//
// A panic raised after the response headers are already on the wire — from inside
// the body write — cannot be turned into a 500 any more; serveFailure will try, and
// net/http will log the superfluous WriteHeader. That is still better than dropping
// the connection, and there is nothing else left to do at that point.
func (h *Handler) serveGuarded(w http.ResponseWriter, r *http.Request, route *routeRef) (status int) {
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
	if h.chain == nil {
		return h.serve(w, r, route)
	}
	h.boundBeforeMiddleware(w, r)

	// Through the middleware, which may answer the request itself — a 401, a
	// redirect — without serve ever running. The status is then whatever it
	// wrote.
	state := &chainState{route: route}
	capture := &statusCapturingWriter{ResponseWriter: w}
	h.chain.ServeHTTP(capture, r.WithContext(context.WithValue(r.Context(), chainStateKey{}, state)))
	if state.status != 0 {
		return state.status
	}
	return capture.Status()
}

// serve runs the request lifecycle and returns the status code it wrote.
//
// route is serveGuarded's record of what the request resolved to, and serve is the
// only frame that can fill it in: it is the frame that reads the match. It does so
// the instant a mount claims the request or the router reports a document, before
// calling anything that could panic on that route's behalf, so every failure from
// that point on — including one caught two frames up in serveGuarded — is answered
// in the content type that route requires. See routeKind.
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
func (h *Handler) serve(w http.ResponseWriter, r *http.Request, route *routeRef) int {
	// Middleware is done, so what the request declared through Vary and
	// SkipCache is settled here, for every route alike. Freezing only where a
	// cache key was computed made a late call an error on a cached page and a
	// silent no-op on any other.
	freezeDeclarations(r)

	for _, mount := range h.mounts {
		if mount.Handles(r.URL.Path) {
			route.resolved(routeKindMount, mount.Prefix())
			route.at(mount.Prefix(), "")
			return h.serveMount(w, r, mount, route)
		}
	}
	for _, mount := range h.handlers {
		if mount.Matches(r.URL.Path) {
			route.resolved(routeKindHandler, mount.Prefix)
			route.at(mount.Prefix, "")
			return h.serveHandler(w, r, mount, route)
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
	// Before the not-found fallback and before Page, for the same reason the
	// document branch is: a matched action leaves Page and Document nil, and the
	// nil-Page check below would read that as a 404.
	if match.Action != nil {
		route.resolved(routeKindAction, match.Action.Name)
		route.at(match.Action.Paths[match.Locale], match.Locale)
		return h.serveAction(w, r, match, route)
	}

	// A path that exists but does not answer this method. Distinct from a 404,
	// and the distinction is the whole point: the reader is told the URL is real
	// and what it does accept, rather than that it is not a URL.
	if match.MethodNotAllowed {
		if match.Document != nil {
			// A document fails as a document, 405 included: a client that
			// asked for /robots.txt is not handed an HTML page.
			route.resolved(routeKindDocument, match.Document.Name)
		}
		w.Header().Set("Allow", strings.Join(match.Allowed, ", "))
		refused := route.failure(http.StatusMethodNotAllowed, stageRoute,
			fmt.Errorf("%w: %s %s", ErrMethodNotAllowed, r.Method, r.URL.Path))
		refused.locale = match.Locale
		return h.serveFailure(w, r, refused)
	}

	// An OPTIONS request that no action claimed. The router already worked out
	// what the path accepts, so answering here keeps one list in one place.
	if r.Method == http.MethodOptions && len(match.Allowed) > 0 {
		header := w.Header()
		header.Set("Allow", strings.Join(match.Allowed, ", "))
		header.Set("Content-Length", "0")
		w.WriteHeader(http.StatusNoContent)
		return http.StatusNoContent
	}

	if match.Document != nil {
		route.resolved(routeKindDocument, match.Document.Name)
		route.at(documentPattern(match.Document, match.Locale), match.Locale)
		return h.serveDocument(w, r, match, route)
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
	route.resolvedPage(page.Name)
	pagePattern, _ := page.PathFor(match.Locale)
	route.at(pagePattern, match.Locale)

	// A warm is no reader, and a guard decides about a reader: asked about a
	// warm, it would refuse it or let it through on nobody's credentials. The
	// page is left to its first allowed reader, as before warming existed.
	warm := warmStateOf(ctx)
	if warm != nil && types.PageGuarded(page) {
		warm.skip(errWarmGuarded)
		return 0
	}

	// Guards before the cache: a blocked reader reaches neither the page's
	// cached render nor the plugins that watch a page being rendered — the
	// request never touched the page.
	if status, allowed := h.checkGuards(w, r, route, match.Locale, types.PageGuards(page)); !allowed {
		return status
	}

	// Only GET and HEAD may be served from cache, and only a cacheable strategy
	// is looked up at all: an unsafe method's response is never a cached page.
	key := ""
	// stale is the entry found past an invalidation or its expiry, kept for the
	// render below to fall back to. Nil when there was none, or nothing was looked up.
	var stale *staleEntry
	cacheable := h.cache != nil && page.Strategy.Cacheable() && (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		!requestSkipsCache(r)
	if cacheable {
		// The query is a cache dimension, not decoration: a fragment's data
		// handler receives the whole *http.Request and may legitimately render
		// from r.URL.Query(), so two queries against one path are two
		// representations. Which parts of it discriminate is the page's own
		// declaration — see queryVary and Page.CacheParams.
		key = cache.Key(cache.KeyInput{
			Path:    r.URL.Path,
			Locale:  match.Locale,
			Params:  match.PathParams,
			Vary:    queryVary(r.URL, page.CacheParams),
			Request: requestVary(r),
			Host:    ascii.LowerString(r.Host),
		})
		lookupStart := time.Now()
		// Never read from the cache in development.
		//
		// Templates reload from disk there, which is the point of dev mode — and
		// a cached page hides that reload for as long as its TTL, on exactly the
		// pages a developer is most likely to be editing. Two mechanisms, each
		// sensible alone, combining into "my edit did nothing".
		//
		// The write path below is left alone deliberately: entries are still
		// stored, tags still tracked, so anyone developing an
		// invalidation rule still sees it work. What
		// dev mode removes is serving a page that was rendered before the edit.
		content, etag, isStale, found := h.cacheGet(ctx, key)
		if found && !isStale {
			h.metrics.CacheEvent(ctx, observability.CacheHit, key)
			// The only place cacheHit is ever reported true: what it times is the
			// lookup that stood in for a render, not a render that did not happen.
			h.metrics.RenderDuration(ctx, page.Name, time.Since(lookupStart), true)
			h.readBy(ctx, key)
			return h.serveCached(w, r, page, content, etag, cacheHit)
		}
		// A stale entry is a miss as far as the cache is concerned: the page
		// renders below as on any miss, and the entry is only what the reader is
		// given if that render cannot replace it. CacheStale is reported when it
		// is actually served, not here.
		if found {
			stale = &staleEntry{content: content, etag: etag}
		}
		h.metrics.CacheEvent(ctx, observability.CacheMiss, key)
	}

	// One render serves every request that wants this key, and the rest wait for
	// it. Without the flight, the moment a popular page expires is the moment
	// every request in flight becomes a cache miss and renders — identical work,
	// multiplied by traffic, aimed at the upstream that has just been shown to be
	// slow. See flight.
	//
	// A page with no key is rendered directly. There is nothing to coalesce on,
	// and a page the application declared dynamic is two renders for two requests
	// by its own declaration.
	var out *outcome
	shared := false
	if key != "" {
		// A shared render belongs to no one request, so it does not end when the
		// request that started it does. Under that request's context, a reader
		// leaving would fail every data handler still running with "context
		// canceled", and the optional parts among them would reach every waiter
		// as a page with holes in it. WithoutCancel detaches it from that, and
		// renderSafeContext then strips the context down to the values every
		// reader of this page shares — the route and the vary set — hiding the
		// per-reader ones an application's middleware may have stored, for the same
		// reason sharedRequest drops the reader's cookies and headers. The locale
		// is not among the context values because it is passed to the render
		// directly (match.Locale); a trace carried in the context does lose its
		// parent here, since a shared render is nobody's request to trace. The
		// render stays bounded by its fragments' own timeouts.
		shareable := sharedRequest(r, page.CacheParams)
		shareable = shareable.WithContext(h.renderSafeContext(shareable.Context()))
		if types.IsCapture(ctx) {
			// A static build's header capture renders on its own, as a shared
			// render would see it but outside the flight: joined to a reader's,
			// it would take that render's cache write as its own, and leading
			// one, it would hand its mark to every reader waiting on it. It
			// keeps its own deadline, too.
			out = h.renderPage(h.renderSafeContext(ctx), shareable, page, match, key, cacheable)
		} else if warm != nil {
			// A warm renders in a flight of its own: see warmFlight. Under its
			// own context, not detached from it, because the warmer's context
			// is no reader's and ends only when shutdown gives up waiting.
			renderCtx := h.renderSafeContext(ctx)
			out, shared = h.warmFlight.do(ctx, key, func() *outcome {
				return h.renderPage(renderCtx, shareable, page, match, key, cacheable)
			})
		} else {
			renderCtx := h.renderSafeContext(context.WithoutCancel(ctx))
			out, shared = h.flight.do(ctx, key, func() *outcome {
				return h.renderPage(renderCtx, shareable, page, match, key, cacheable)
			})
		}
	} else {
		out = h.renderPage(ctx, r, page, match, key, cacheable)
	}
	if shared {
		// Not a hit: nothing was in the cache when this request asked. It is the
		// other outcome that costs nothing, and it is worth being able to see
		// separately — a large coalesced count is what a too-short TTL looks
		// like from the outside.
		h.metrics.CacheEvent(ctx, observability.CacheCoalesced, key)
	}
	if warm != nil {
		warm.observe(out, shared)
	}

	// Decided per request, after the flight: every request that joined a render
	// made its own lookup, so each one falls back to the stale entry it found,
	// and one that found none is answered by the render as on any miss.
	if stale != nil {
		switch h.staleFallback(ctx, out) {
		case fallbackStale:
			// Reported once the stale page went out: serveCached can still fail
			// to personalise it, and then what was served is an error page.
			status := h.serveCached(w, r, page, stale.content, stale.etag, cacheStale)
			if status == http.StatusOK || status == http.StatusNotModified {
				h.readBy(ctx, key)
				h.reportStale(r, key, "page", page.Name, out)
			}
			return status
		case fallbackDrop:
			// The content is gone, so the stale copy of it goes too: kept, it
			// would be served the next time a render merely failed. Without the
			// request's cancellation: the drop is for every later reader, and a
			// reader who left must not keep the entry alive.
			_ = h.cache.InvalidateKey(context.WithoutCancel(ctx), key)
		}
	}

	if out.fail != nil {
		return h.serveFailure(w, r, *out.fail)
	}

	p, err := h.personalise(w, r, out.content, out.etag)
	if err != nil {
		return h.serveFailure(w, r, failureFor(r, http.StatusInternalServerError, stagePlugin, err))
	}
	content, etag, personal := p.body, p.etag, p.personal

	header := w.Header()
	header.Set("Content-Type", contentTypeHTML)
	header.Set("ETag", etag)
	h.setCacheHeaders(header, r, page.Strategy, page.CacheTTL)
	guardedCacheControl(header, page)
	// Not written to this server's cache, so not to any cache in front of it:
	// a public header would pin one request's fragment failure at a CDN for
	// the whole TTL, which is what keeping it out of the cache was for.
	if out.degraded {
		header.Set("Cache-Control", "no-store")
	}
	// After setCacheHeaders, so it overrides whatever the page's strategy declared.
	// The body that goes on the wire carries this reader's token, whatever the
	// shared one behind it may be cached as.
	if personal {
		header.Set("Cache-Control", "private, no-store")
	}
	if h.devMode {
		header.Set(renderTimeHeader, out.renderTime.String())
	}
	// Here rather than with the other headers above: personalise can still fail,
	// and the failure page answering instead must not claim to be a render.
	if key != "" && h.reportsCacheStatus(ctx) {
		header.Set(CacheStatusHeader, cacheMiss)
	}
	content = withDevOverlay(content, overlayHeading(out.problems), out.problems)
	content = h.withDevReload(r, content)
	w.WriteHeader(http.StatusOK)
	writeBody(w, content)

	return http.StatusOK
}

// withDevReload adds the live-reload script to a page in development.
//
// Only to the answer to a GET. Reloading the answer to a POST asks the browser to
// submit the form again, which is the last thing a page should do on its own. And
// not when the URL asks to be left alone with ?collage-reload=0.
func (h *Handler) withDevReload(r *http.Request, html []byte) []byte {
	if h.reload == nil || r.Method != http.MethodGet || r.URL.Query().Get(devReloadOptOut) == "0" {
		return html
	}
	return withReloadScript(html, h.reload.version())
}

// CloseDevStreams ends every development reload stream. A graceful shutdown waits
// for open requests, and these never finish by themselves.
func (h *Handler) CloseDevStreams() {
	if h.reload != nil {
		h.reload.Close()
	}
}

// personalised is one body made ready for one reader.
type personalised struct {
	body []byte
	etag string
	// personal: the body carries this reader's forgery token, or a hook made it
	// particular to them.
	personal bool
	// hookPersonal: a hook set Personal. Unlike a token, which a handler's own
	// Cache-Control has always been trusted around, this overrides it.
	hookPersonal bool
}

// personalise replaces the forgery-token marker in content with this reader's own
// token, and sends the cookie the token is checked against.
//
// This is what lets a page with a form be cached. What is stored, shared between
// readers and handed to everyone waiting on one render, is a body with a marker in
// it — a string nobody can compute without the application's key. What goes on the
// wire is that body with the reader's own token in place of the marker.
//
// A body no one rewrites is returned untouched, which is every page that has no form
// on a site with no PersonaliseHook plugin.
//
// Then every PersonaliseHook plugin rewrites the body for this reader, in
// registration order; a hook that fails is returned as the error, for the caller to
// answer as its kind of response must. The content returned with an error is the
// one before any hook ran, never a partly rewritten one, and the token's cookie is
// not sent: the error page answering instead sends its own if it carries a form.
func (h *Handler) personalise(w http.ResponseWriter, r *http.Request, content []byte, etag string) (personalised, error) {
	original := personalised{body: content, etag: etag}
	// A warm's body is thrown away, so making it a reader's is work for nobody,
	// and a hook would count a reader who was never there.
	if warmStateOf(r.Context()) != nil {
		return original, nil
	}
	var cookie *http.Cookie
	if h.csrf != nil && h.csrf.Carries(content) {
		token, _, err := h.csrf.TokenFor(r)
		if err != nil {
			// Nothing to substitute with. Serving the marker would render a form
			// that is refused on submission with nothing to explain why, so this
			// is a failure rather than a body.
			h.logger.Error("collage: could not issue a forgery token", "err", err)
		} else {
			content = h.csrf.Personalise(content, token)
			cookie = h.csrf.Cookie(r, token)
		}
	}
	personal := cookie != nil

	ev := &plugin.PersonaliseEvent{Request: r, Header: w.Header(), Body: content}
	if err := h.plugins.Personalise(r.Context(), ev); err != nil {
		return original, err
	}
	if cookie != nil {
		http.SetCookie(w, cookie)
	}
	changed := personal || !bytes.Equal(ev.Body, content)
	// A static build's capture asks what the exported file is to be served
	// with, and that file is no reader's: the build writes it from the render,
	// not from this answer, and a header a hook made per response — a nonce —
	// differs between the build's two answers and is left out. So its
	// Cache-Control is the one the page's own strategy gives, which lets a host
	// cache the file. The hooks still ran; only what their Personal decides is
	// set aside. A forgery token is not a hook's, and still makes it personal.
	hookPersonal := ev.Personal && !types.IsCapture(r.Context())
	out := personalised{
		body:         ev.Body,
		etag:         etag,
		personal:     personal || hookPersonal,
		hookPersonal: hookPersonal,
	}
	// Recomputed whenever the body is not the one the ETag was made from. An ETag
	// that names a body nobody was sent is how a conditional request is answered
	// 304 for content the client never had.
	if changed || ev.Personal {
		out.etag = cache.ETag(out.body)
	}
	return out, nil
}

// renderPage renders one page and returns what to write, or why nothing can be.
//
// It writes nothing itself, and that is the point: the work may be being done on
// behalf of several requests at once, and each of them writes its own response from
// the value this returns.
func (h *Handler) renderPage(
	ctx context.Context,
	r *http.Request,
	page *types.Page,
	match *router.MatchResult,
	key string,
	cacheable bool,
) *outcome {
	// Read before anything renders, so an invalidation under way as it begins,
	// or begun at any point during it, keeps it out of the cache. See writeCache.
	// A shared store's epoch only for a render that may be written, so a page
	// that never is costs it no round trip.
	mark := h.localMark()
	if cacheable && r.Method == http.MethodGet && !types.IsCapture(ctx) {
		mark = h.invalidationMark(ctx)
	}

	// Deliberately after the cache lookup: BeforeRenderHook documents that it
	// does not fire when a cached render is served instead of a fresh one.
	// Built before the hook, not after: a plugin contributing to the page hoists
	// through this, and hoisting only works before the tree renders.
	rc := types.NewRenderContext(ctx, r, page, match.Locale, match.PathParams)
	if skipsCache(r) {
		// A preview skips the data cache as well as the page cache: an editor
		// shown the draft page around a cached copy of the published data has
		// not been shown the draft.
		types.SkipDataCache(rc)
	}

	if err := h.plugins.BeforeRender(ctx, &plugin.BeforeRenderEvent{
		Context: rc,
		Page:    page,
		Locale:  match.Locale,
		Path:    r.URL.Path,
	}); err != nil {
		return &outcome{fail: &failure{
			status: http.StatusInternalServerError,
			err:    err,
			page:   page,
			locale: match.Locale,
			stage:  stageBeforeRender,
		}}
	}

	renderStart := time.Now()
	result, err := h.renderer.Render(ctx, rc)
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
		return &outcome{fail: &failure{
			status:   status,
			err:      err,
			page:     page,
			locale:   match.Locale,
			fragment: failedFragment(result),
			stage:    stageRender,
		}}
	}

	// The hook may replace the HTML, and the replacement is what goes on the wire
	// and, below, into the cache.
	afterRender := &plugin.AfterRenderEvent{
		Page:           page,
		Locale:         match.Locale,
		Path:           r.URL.Path,
		Degraded:       result.Degraded(),
		Fragments:      render.FragmentReports(result),
		DependencyTags: append([]string(nil), result.DependencyTags...),
		Values:         types.ValuesOf(rc),
		HTML:           result.HTML,
	}
	plugin.PrepareHoist(afterRender, render.HoistEnds(result), types.RenderHoisted(rc))
	if err := h.plugins.AfterRender(ctx, afterRender); err != nil {
		// No fragment is named: the failure is the plugin's, and pointing the dev
		// page at a fragment that merely happened to be degraded would send a
		// developer looking in the wrong place.
		return &outcome{fail: &failure{
			status: http.StatusInternalServerError,
			err:    err,
			page:   page,
			locale: match.Locale,
			stage:  stageAfterRender,
		}}
	}
	content := afterRender.HTML
	if len(content) == 0 {
		// A blank page is not a page. See ErrEmptyRender.
		return &outcome{fail: &failure{
			status: http.StatusInternalServerError,
			err:    fmt.Errorf("%w: page %q", ErrEmptyRender, page.Name),
			page:   page,
			locale: match.Locale,
			stage:  stageRender,
		}}
	}

	// A degraded render is complete enough to serve but must never be cached:
	// caching it would pin one request's transient fragment failure in front of
	// every later request. A HEAD is served from cache but never populates it —
	// it produced no body to store.
	etag := ""
	if cacheable && r.Method == http.MethodGet && !result.Degraded() {
		etag = h.writeCache(r, key, page, content, result.DependencyTags, mark)
	}
	if etag == "" {
		etag = cache.ETag(content)
	}

	problems := append(h.devProblems(result), h.devFindings(afterRender.Findings)...)
	return &outcome{
		content:       content,
		etag:          etag,
		renderTime:    renderTime,
		problems:      problems,
		degraded:      result.Degraded(),
		staleDataOnly: result.Stale && !anyFragmentFailed(result),
	}
}

// cacheGet is the cache lookup, which never finds anything in development. See the
// call site for why.
//
// Against a cache.StaleCache it also finds an entry kept past an invalidation or
// its expiry, reported with stale true, for the caller to fall back to. It calls
// GetStale alone, never Get first: one lookup is one hit or one miss in the
// cache's own statistics. Any other Cache reports only fresh entries, exactly as
// before there was such a thing as a stale page.
func (h *Handler) cacheGet(ctx context.Context, key string) (content []byte, etag string, stale, found bool) {
	if h.devMode {
		return nil, "", false, false
	}
	if staleCache, ok := h.cache.(cache.StaleCache); ok {
		content, etag, stale, found = staleCache.GetStale(ctx, key)
	} else {
		content, etag, found = h.cache.Get(ctx, key)
	}
	if found && h.staleMarker(content) {
		// Stored under another forgery key — before a key change, or by a process
		// that generated its own. Served, its form would carry a marker nothing
		// replaces and be refused on submission, so it is a miss, and dropped. A
		// stale entry is no different: it is served as it was stored.
		_ = h.cache.InvalidateKey(ctx, key)
		return nil, "", false, false
	}
	return content, etag, stale, found
}

// staleEntry is a cache entry found past an invalidation or its expiry: what a
// request falls back to when the render replacing it does not succeed.
type staleEntry struct {
	content []byte
	etag    string
}

// fallback is what a request holding a stale entry does with the render's outcome.
type fallback int

const (
	// fallbackNone serves the outcome, a render or its failure, as on a miss.
	fallbackNone fallback = iota
	// fallbackStale serves the stale entry instead of the outcome.
	fallbackStale
	// fallbackDrop drops the stale entry, then serves the outcome.
	fallbackDrop
)

// staleFallback decides what a request that found a stale entry does with the
// outcome of the render meant to replace it.
//
// A render that succeeded is served: it is fresher than the entry, and it has
// already been written over it unless an invalidation overlapped it. A not found
// is served too, and the entry dropped: the content is gone upstream, and the
// reader is told so rather than shown it. Any other failure, or a degraded render,
// is served the stale entry instead: the old page whole is better than an error
// page or the new one with holes in it, which is the whole of the stale cache.
//
// Except for a request that has ended. Its failure is the flight reporting that
// the reader left, which is no failure of the page, and nobody reads the answer.
func (h *Handler) staleFallback(ctx context.Context, out *outcome) fallback {
	switch {
	case out.fail != nil && out.fail.status == http.StatusNotFound:
		return fallbackDrop
	case out.fail != nil && ctx.Err() != nil:
		return fallbackNone
	case out.fail != nil, out.degraded:
		return fallbackStale
	}
	return fallbackNone
}

// reportStale accounts for one stale page or document served in place of out: a
// stale serve is the site hiding an outage, and the outage must stay visible to
// whoever watches the site.
//
// CacheStale is reported for every stale response, since it counts serves. The
// log line, and the render's failure for the error hooks, are reported once per
// render: every request that joined one render in the flight holds the same
// outcome, and the first of them to serve stale claims it. Without that, eight
// readers waiting on one failed render would be eight outages to an error
// tracker.
//
// The line's level says why. A render that failed, or that has a failed fragment,
// is a warning. A render whose only fault is stale cached data is no outage: the
// data store is refreshing what it read in the background, and the next render
// is likely whole, so it is a debug line.
//
// A failure goes to the error hooks with a zero status, as a failed cache
// write's does, because the response actually sent is the stale page, not an
// error. It is not also logged as "request failed": the warning is its one line.
// A degraded render has no failure to hand on: its failed fragments were already
// logged and measured by the render engine, which is how a fragment failure is
// reported whether or not the page is then served stale.
func (h *Handler) reportStale(r *http.Request, key, kind, name string, out *outcome) {
	// A warm's stale serve went to nobody, so it is no serve to count, and the
	// warmer reports its failure itself, once per run rather than per page.
	if warm := warmStateOf(r.Context()); warm != nil {
		warm.record(out)
		return
	}
	h.metrics.CacheEvent(r.Context(), observability.CacheStale, key)
	if !out.staleReported.CompareAndSwap(false, true) {
		return
	}
	if out.fail == nil && out.staleDataOnly {
		h.logger.Debug("collage: serving a stale "+kind+" while its data refreshes",
			kind, name, "path", r.URL.Path)
		return
	}
	reason := "degraded"
	if out.fail != nil && out.fail.err != nil {
		reason = out.fail.err.Error()
	}
	h.logger.Warn("collage: serving a stale "+kind+", its render failed",
		kind, name, "path", r.URL.Path, "err", reason)
	if out.fail != nil {
		hidden := *out.fail
		hidden.status = 0
		h.dispatchError(r, hidden)
	}
}

// anyFragmentFailed reports whether any fragment of result failed. It is
// nil-safe.
func anyFragmentFailed(result *render.Result) bool {
	if result == nil || result.Metadata == nil {
		return false
	}
	for i := range result.Metadata.Fragments {
		if result.Metadata.Fragments[i].Failed {
			return true
		}
	}
	return false
}

// reportsCacheStatus reports whether a cacheable response — one with a cache key,
// which the caller checks — carries CacheStatusHeader. Never in development,
// where the cache is not read, nor to a static build's header capture, whose
// answers are not a reader's and whose headers are what an exported site is
// served with.
func (h *Handler) reportsCacheStatus(ctx context.Context) bool {
	return !h.devMode && !types.IsCapture(ctx)
}

// invalidationMark is the invalidations counted started and finished when a
// render began, and, when the cache is a SharedCache, the store's epoch then.
type invalidationMark struct {
	started, finished uint64
	// shared is the SharedCache's epoch, which the write is weighed against
	// (see writeShared), valid when sharedOK is: false when the cache is not a
	// SharedCache, when the mark is local only, or when the epoch could not be
	// read.
	shared   uint64
	sharedOK bool
}

// localMark reads this process's invalidation counts now, or zeros when there
// is no such rule. It never reaches the cache: it is what the checks after a
// write and the warmer's bookkeeping compare against.
func (h *Handler) localMark() invalidationMark {
	if h.invalidations == nil {
		return invalidationMark{}
	}
	started, finished := h.invalidations()
	return invalidationMark{started: started, finished: finished}
}

// invalidationMark reads the mark a render that may be written begins at: the
// local counts, and a SharedCache's epoch, under sharedStoreTimeout. A failed
// read leaves sharedOK false, which skips the write; it is not reported, as
// the store being down is not this request's failure.
func (h *Handler) invalidationMark(ctx context.Context) invalidationMark {
	m := h.localMark()
	shared, ok := h.cache.(cache.SharedCache)
	if !ok {
		return m
	}
	epochCtx, cancel := context.WithTimeout(ctx, sharedStoreTimeout)
	defer cancel()
	if epoch, err := shared.Epoch(epochCtx); err == nil {
		m.shared, m.sharedOK = epoch, true
	}
	return m
}

// storable reports whether a render that began at m may be stored now: no
// invalidation was under way when it began, and none has begun since.
func (h *Handler) storable(m invalidationMark) bool {
	return m.started == m.finished && !h.invalidatedSince(m)
}

// invalidatedSince reports whether an invalidation has begun since m was read.
func (h *Handler) invalidatedSince(m invalidationMark) bool {
	return h.localMark().started != m.started
}

// foreignMarker matches the forgery-token marker of any key.
var foreignMarker = regexp.MustCompile(`collage-csrf-[A-Za-z0-9_-]{32}`)

// staleMarker reports whether content carries a forgery-token marker this
// application would not replace: another key's, or any at all when forgery
// protection is off.
func (h *Handler) staleMarker(content []byte) bool {
	if !bytes.Contains(content, []byte("collage-csrf-")) {
		return false
	}
	current := ""
	if h.csrf != nil {
		current = h.csrf.Marker()
	}
	for _, found := range foreignMarker.FindAll(content, -1) {
		if string(found) != current {
			return true
		}
	}
	return false
}

// serveCached writes a cache hit: a 304 when the request's If-None-Match matches
// the stored ETag, otherwise a 200 carrying the stored content. It returns the
// status it wrote.
//
// status is the CacheStatusHeader value, cacheHit or cacheStale. A stale entry is
// served with no-store: it is this server's fallback while the page cannot be
// re-made, and a CDN keeping it would go on serving it after the page could.
func (h *Handler) serveCached(w http.ResponseWriter, r *http.Request, page *types.Page, content []byte, etag, status string) int {
	// The stored body carries a marker where this reader's forgery token goes, so
	// what is written is not what was stored — and the ETag has to name what was
	// written, or a conditional request is answered 304 for a body the client was
	// never sent.
	p, err := h.personalise(w, r, content, etag)
	if err != nil {
		return h.serveFailure(w, r, failureFor(r, http.StatusInternalServerError, stagePlugin, err))
	}
	content, etag, personal := p.body, p.etag, p.personal

	header := w.Header()
	header.Set("ETag", etag)
	h.setCacheHeaders(header, r, page.Strategy, page.CacheTTL)
	guardedCacheControl(header, page)
	if status == cacheStale {
		header.Set("Cache-Control", "no-store")
	}
	if personal {
		header.Set("Cache-Control", "private, no-store")
	}
	// After personalise, which can still fail: the failure page answering
	// instead is no cache hit. Before the 304, which is one.
	if h.reportsCacheStatus(r.Context()) {
		header.Set(CacheStatusHeader, status)
	}

	// Never for a body a hook made personal: its ETag is new on every response,
	// so only "*" could match, and a 304 would have the client keep a body
	// carrying another response's nonce. A forgery token alone keeps the 304: a
	// returning reader's token, and so the ETag, repeats.
	if !p.hookPersonal && cache.ETagMatch(r.Header.Get("If-None-Match"), etag) {
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

// writeCache stores content under key, and records the key's tags with the tracker.
// Both write paths track: SetTagged indexes the tags inside the cache and Track
// indexes them in the tracker, and invalidation may go through either, so the two
// indexes are kept deliberately redundant.
//
// A failure here never fails the request. The page has already rendered, and
// serving it uncached is strictly better than turning a cache problem into a 500.
//
// It returns the ETag the cache stored the entry under, or the empty string when
// nothing was written. Cache is an interface the application may implement, and its
// contract is that Set returns the content's ETag — so the stored value, not a
// locally recomputed one, is what the fresh response must advertise. A cache that
// derives ETags its own way would otherwise send one value on the fresh response
// and a different one on every hit that followed, and every conditional request
// would miss.
//
// mark is the invalidation counts read before the render began. A render that an
// invalidation or a purge overlapped — under way as it began, or begun before its
// write — may have read what was invalidated, so it is not written: stored, it
// would be served as fresh for its whole TTL, and the invalidation would be lost.
// It is still served to the requests it was made for.
//
// A SharedCache is written through writeShared instead, which weighs the write
// against the store's epoch as well, so an invalidation on any instance counts.
func (h *Handler) writeCache(r *http.Request, key string, page *types.Page, content []byte, tags []string, mark invalidationMark) string {
	ctx := r.Context()
	// A static build asking what it answers is not a reader: the build has
	// written the file, and an entry or a hook call on its behalf would be
	// state the build left behind.
	if types.IsCapture(ctx) {
		return ""
	}
	if shared, ok := h.cache.(cache.SharedCache); ok {
		report := func(err error) {
			h.reportError(r, failure{err: err, page: page, stage: stageCacheWrite})
		}
		return h.writeShared(r, shared, key, content, h.ttlFor(page.Strategy, page.CacheTTL), tags,
			newRecipe(r, page.CacheParams, false), mark, report)
	}
	if !h.storable(mark) {
		h.overlapped(ctx, mark)
		return ""
	}

	ttl := h.ttlFor(page.Strategy, page.CacheTTL)

	var etag string
	var err error
	if tagged, ok := h.cache.(cache.TaggedCache); ok {
		etag, err = tagged.SetTagged(ctx, key, content, ttl, tags)
	} else {
		etag, err = h.cache.Set(ctx, key, content, ttl)
	}
	if err != nil {
		h.reportError(r, failure{err: fmt.Errorf("collage: cache write: %w", err), page: page, stage: stageCacheWrite})
		return ""
	}
	h.metrics.CacheEvent(ctx, observability.CacheSet, key)
	// r is the shared request the render was made from, so the recipe holds what
	// the key holds and nothing of the reader. See warmKey. Not with warming
	// off, when nothing reads it.
	if h.warm != nil {
		h.recipes.put(key, newRecipe(r, page.CacheParams, false), warmStateOf(ctx) == nil)
	}

	if err := h.tracker.Track(ctx, key, withRouteTags(tags, r.Host, r.URL.Path)); err != nil {
		h.reportError(r, failure{err: fmt.Errorf("collage: track %q: %w", key, err), page: page, stage: stageCacheWrite})
	}
	h.wroteForWarm(ctx, key, h.dropIfOverlapped(ctx, key, mark))
	return etag
}

// wroteForWarm clears the read mark of a key a warm wrote, when the write stood:
// until a reader has it, the next invalidation passes it over (see
// errWarmUnread). A write an overlap dropped leaves the mark as it was.
func (h *Handler) wroteForWarm(ctx context.Context, key string, dropped bool) {
	if h.warm != nil && !dropped && warmStateOf(ctx) != nil {
		h.recipes.clearRead(key)
	}
}

// readBy marks key read when ctx is a reader's: a page or document served to
// one, fresh or stale. Not for a warm's own lookup, nor a static build's
// capture, and not with warming off, when nothing reads the mark.
func (h *Handler) readBy(ctx context.Context, key string) {
	if h.warm == nil || warmStateOf(ctx) != nil || types.IsCapture(ctx) {
		return
	}
	h.recipes.markRead(key)
}

// overlapped records, on a warm's render, that an invalidation overlapped the
// render begun at mark and its entry was not kept, so the warmer queues the key
// again (see requeue). A reader's render records nothing: its key is rendered
// again on its next request.
func (h *Handler) overlapped(ctx context.Context, mark invalidationMark) {
	if warm := warmStateOf(ctx); warm != nil {
		warm.overlap(mark)
	}
}

// dropIfOverlapped drops the entry just written for key when an invalidation or a
// purge began between the check before the write and the write itself: the check
// alone leaves that window, and the invalidation, finding nothing yet, would leave
// the entry written after it fresh. Dropped rather than marked stale, because
// which of the two it was is not known, and a purge leaves nothing behind.
//
// A SharedCache's write goes through staleIfOverlapped instead.
//
// It reports whether it dropped the entry.
func (h *Handler) dropIfOverlapped(ctx context.Context, key string, mark invalidationMark) bool {
	if !h.invalidatedSince(mark) {
		return false
	}
	_ = h.cache.InvalidateKey(context.WithoutCancel(ctx), key)
	h.overlapped(ctx, mark)
	return true
}

// documentPattern is the pattern doc answers in locale: its own path there, or
// its path outside every locale.
func documentPattern(doc *types.Document, locale string) string {
	if pattern, ok := doc.PathFor(locale); ok {
		return pattern
	}
	pattern, _ := doc.RootPath()
	return pattern
}

// cleanPath returns p with dot segments resolved and repeated slashes collapsed,
// its trailing slash kept, and whether that differs from p.
func cleanPath(p string) (string, bool) {
	if p == "" {
		return "/", true
	}
	if !strings.Contains(p, "//") && !strings.Contains(p, "/.") {
		return p, false
	}
	cleaned := path.Clean(p)
	if strings.HasSuffix(p, "/") && cleaned != "/" {
		cleaned += "/"
	}
	return cleaned, cleaned != p
}

// redirectClean sends the request to the clean spelling of its path, as
// net/http's ServeMux does: 301 for a read, 308 for anything that carries a body,
// so a form is posted again rather than turned into a GET. It returns the status
// it wrote, for the request hooks' finish.
func redirectClean(w http.ResponseWriter, r *http.Request, cleaned string) int {
	status := http.StatusPermanentRedirect
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		status = http.StatusMovedPermanently
	}
	w.Header().Set("Location", cleanLocation(cleaned, r.URL.RawQuery))
	w.WriteHeader(status)
	return status
}

// cleanLocation spells the clean path as a Location, escaped. The path was
// cleaned decoded, so written as it is it would be read again: "/./%5Cevil.com"
// cleans to "/\evil.com", which a browser takes for "//evil.com", another site,
// and "%3F" would come back as the "?" that starts a query. Escaped, what the
// browser asks for next is the path that was cleaned.
func cleanLocation(cleaned, rawQuery string) string {
	target := (&url.URL{Path: cleaned}).EscapedPath()
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	return target
}

// withRouteTags returns tags plus the tags naming the entry's path and host, so an
// invalidation can say which URL, on which host, it dropped. Kept out of what
// the page declares, since they are the framework's, not the page's.
func withRouteTags(tags []string, host, path string) []string {
	out := make([]string, 0, len(tags)+2)
	out = append(out, tags...)
	return append(out, types.PathTag(path), types.HostTag(ascii.LowerString(host)))
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
// naming the headers the request declared through Vary.
//
// Vary belongs only on a public response: it tells a shared cache which request
// headers select between representations, and a no-store response has no
// representation to select. Shared by the page and document paths; see ttlFor for
// why it is parameterized rather than typed on either route kind.
func (h *Handler) setCacheHeaders(header http.Header, r *http.Request, strategy types.RenderStrategy, cacheTTL time.Duration) {
	control := h.cacheControl(strategy, cacheTTL)
	switch {
	case skipsCache(r):
		// A preview's page is one reader's, and must not be kept by anything
		// between the server and them either.
		control = "private, no-store"
	case h.devMode:
		// Development reads no cache of its own, so that the next request
		// shows the next edit; a browser or a proxy keeping the page would
		// undo that, and keep the dev overlay's error text besides.
		control = "no-store"
	}
	header.Set("Cache-Control", control)

	if !strings.HasPrefix(control, "public") {
		return
	}
	addVary(header, requestVaryHeaders(r)...)
}

// addVary adds names to header's Vary, beside what is already there. Set would
// drop a Vary a middleware wrote — say for a header it answers by — and a CDN
// would then serve one reader's copy to the next.
func addVary(header http.Header, names ...string) {
	var have []string
	for _, value := range header.Values("Vary") {
		for name := range strings.SplitSeq(value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				have = append(have, name)
			}
		}
	}
	added := false
	for _, name := range names {
		if !slices.ContainsFunc(have, func(h string) bool { return h == "*" || ascii.EqualFold(h, name) }) {
			have = append(have, name)
			added = true
		}
	}
	if added {
		header.Set("Vary", strings.Join(have, ", "))
	}
}

// cacheControl returns the Cache-Control value for a route with the given render
// strategy: static is cached but always revalidated, incremental is cached for its
// effective TTL, and dynamic is never stored. The incremental value is derived from
// cacheTTL, so the header a client sees does not change from request to request.
// Shared by the page and document paths; see ttlFor for why it is parameterized
// rather than typed on either route kind.
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
