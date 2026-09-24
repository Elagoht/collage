// Package render composes a page's fragment tree into HTML. It walks the tree
// depth-first and strictly sequentially, expanding each {{slot "name"}} into the
// rendered output of the fragments bound to that slot, isolating fragment failures
// according to the framework's failure policy, and collecting the dependency tags the
// render relied on. Sequential execution is a design choice, not an oversight: the
// same page rendered twice from the same inputs must produce byte-identical output.
package render

import (
	"context"
	"fmt"
	"time"

	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/template"
	"github.com/Elagoht/collage/internal/types"
)

// DefaultMaxDepth is the fragment nesting depth Options.MaxDepth falls back to.
const DefaultMaxDepth = 32

// DefaultTimeout is the data handler timeout Options.DefaultTimeout falls back to.
const DefaultTimeout = 5 * time.Second

// Engine renders a page into HTML.
type Engine interface {
	// Render composes rc.Page's fragment tree into a Result. It returns an error
	// only when the render failed as a whole; an isolated fragment failure is
	// reported through the Result instead, see Result.Degraded.
	Render(ctx context.Context, rc *types.RenderContext) (*Result, error)
	// RenderFragment renders one fragment and its subtree on its own, with no
	// page around it. It is what answers a request for part of a page.
	RenderFragment(ctx context.Context, rc *types.RenderContext, f *types.Fragment) ([]byte, error)
}

// Result is one page's rendered output.
type Result struct {
	// HTML is the rendered page. It is nil whenever Render returned an error, and
	// may also be nil on a successful render that produced nothing — an optional
	// root fragment that failed with no fallback renders an empty page rather than
	// failing it. A nil HTML therefore means "no markup", not "error"; Degraded
	// reports whether anything went wrong.
	HTML []byte
	// DependencyTags holds every tag the render depended on: the tags returned by
	// each fragment's data handler plus the page's own, de-duplicated and sorted so
	// the same render always reports the same slice. Empty tags are dropped, as
	// they identify nothing to invalidate.
	DependencyTags []string
	// Metadata describes how the render performed.
	Metadata *Metadata
	// NotFound reports that the render failed because a required fragment's data
	// handler returned an error satisfying errors.Is(err, types.ErrNotFound): the
	// content the page needed does not exist, as distinct from some other kind of
	// failure. This is a classification of the error Render already returned, not
	// a separate success path — HTML is still nil and the caller must still check
	// the error before consulting this field. A non-required fragment's
	// ErrNotFound follows the ordinary optional-failure policy and never sets it.
	NotFound bool
}

// Degraded reports whether any fragment in the render failed, whether or not a
// fallback covered for it. A degraded result is complete enough to serve but must
// not be cached: caching it would pin one request's transient failure in front of
// every later request. It is nil-safe.
func (r *Result) Degraded() bool {
	if r == nil || r.Metadata == nil {
		return false
	}
	for i := range r.Metadata.Fragments {
		if r.Metadata.Fragments[i].Failed {
			return true
		}
	}
	return false
}

// Metadata describes how a render performed.
type Metadata struct {
	// Page is the name of the page that was rendered.
	Page string
	// Locale is the locale the page was rendered for.
	Locale string
	// Timing records where the render spent its time.
	Timing observability.Timing
	// Fragments holds one entry per fragment the render actually ran, in the order
	// the fragments were entered: a parent precedes the children bound into its
	// slots. A fragment rejected before it ran has no entry — the frame that trips
	// MaxDepth, or a nil entry in a Fill slice — since there is nothing to report
	// about a fragment that never executed; the error names it instead. A fallback
	// has no entry of its own either: it is reported on the fragment it stood in
	// for, through UsedFallback. Fragments bound into a fallback's slots do get one.
	Fragments []FragmentMetadata
}

// FragmentMetadata records one fragment's contribution to a render.
type FragmentMetadata struct {
	// Name is the fragment's name.
	Name string
	// Duration is the fragment's wall-clock time, including the time taken by every
	// fragment bound into its slots, since those render inside its template.
	Duration time.Duration
	// Failed reports whether the fragment's own render failed, including when a
	// fallback then produced output in its place.
	Failed bool
	// UsedFallback reports whether the fragment's output came from its fallback.
	UsedFallback bool
	// Err is the error the fragment failed with, or nil.
	Err error
}

// Options configures a SlotEngine.
type Options struct {
	// MaxDepth is the deepest fragment nesting the engine will render, counting the
	// root as the first level. Zero or less selects DefaultMaxDepth.
	MaxDepth int
	// DefaultTimeout bounds a data handler that does not set its own
	// Fragment.Timeout. Zero or less selects DefaultTimeout.
	DefaultTimeout time.Duration
	// Metrics receives per-render and per-fragment timings. Nil means no metrics.
	Metrics observability.Metrics
	// Tracer starts a span per render and per fragment. Nil means no tracing.
	Tracer observability.Tracer
	// DevMode replaces a failed fragment's empty output with an HTML comment naming
	// the fragment and its error, so a failure shows up in the page being developed
	// instead of looking like a section someone forgot to write.
	DevMode bool
	// AssetURL resolves a mounted file's URL to its content-addressed one, and is
	// what backs {{asset "/static/app.css"}}. Nil leaves the template function
	// reporting that no mount can answer, which is what a page linking an asset
	// through an application that mounted none should say.
	AssetURL func(urlPath string) (string, error)
	// CSRFMarker returns the placeholder {{csrfToken}} renders in place of a real
	// token, which the response layer replaces per reader. Nil leaves the template
	// function reporting that forgery protection is off.
	CSRFMarker func() (string, error)
	// URL builds the path of the page or document registered as name, in
	// locale, and is what backs {{pageURL}}, {{pageURLIn}} and {{localeURL}}.
	// Nil leaves those functions reporting that no routes are known.
	URL func(name, locale string, params map[string]string) (string, error)
	// DefaultLocale is the locale {{pageURL}} falls back to for a route with no
	// path in the render's own.
	DefaultLocale string
	// DataCache keeps what collage.Cached fetches across renders. Nil leaves
	// Cached sharing within one render only.
	DataCache types.DataCache
}

// SlotEngine is the Engine implementation that resolves {{slot "name"}} against the
// fragment tree. It holds no per-render state, so one SlotEngine is safe for
// concurrent use by as many renders as the caller likes.
type SlotEngine struct {
	tmpl           template.Engine
	maxDepth       int
	defaultTimeout time.Duration
	metrics        observability.Metrics
	tracer         observability.Tracer
	devMode        bool
	assetURL       func(string) (string, error)
	csrfMarker     func() (string, error)
	url            func(name, locale string, params map[string]string) (string, error)
	defaultLocale  string
	dataCache      types.DataCache
}

var _ Engine = (*SlotEngine)(nil)

// New returns a SlotEngine rendering through tmpl, applying the documented defaults
// for any zero-valued option.
func New(tmpl template.Engine, opts Options) *SlotEngine {
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = DefaultMaxDepth
	}
	if opts.DefaultTimeout <= 0 {
		opts.DefaultTimeout = DefaultTimeout
	}
	return &SlotEngine{
		tmpl:           tmpl,
		maxDepth:       opts.MaxDepth,
		defaultTimeout: opts.DefaultTimeout,
		metrics:        observability.MetricsOrNoop(opts.Metrics),
		tracer:         observability.TracerOrNoop(opts.Tracer),
		devMode:        opts.DevMode,
		assetURL:       opts.AssetURL,
		csrfMarker:     opts.CSRFMarker,
		url:            opts.URL,
		defaultLocale:  opts.DefaultLocale,
		dataCache:      opts.DataCache,
	}
}

// Render composes rc.Page's fragment tree, starting from rc.Page.Root. ctx governs
// the render and replaces the one rc was built with, so a caller cannot accidentally
// hand fragments a context that outlives the request; a nil ctx falls back to rc's.
//
// Render always returns a non-nil Result with non-nil Metadata, on every path: every
// render has timing metadata, including one that failed, which is the render an
// operator most wants to see. Callers MUST check the error before touching HTML — on
// a failure HTML is nil and the Result carries only what was collected before the
// failure, so treating it as a renderable page would serve a blank one.
//
// It returns ErrNilRenderContext for a nil rc or a rc with no Page,
// ErrNoRootFragment for a page with neither a layout nor a content fragment, and the
// underlying error when a required fragment fails or the tree exceeds MaxDepth.
// Every other fragment failure is contained and reported through the Result.
func (e *SlotEngine) Render(ctx context.Context, rc *types.RenderContext) (*Result, error) {
	if rc == nil || rc.Page == nil {
		// Nothing to describe: there is no page to name and no render to time.
		return &Result{Metadata: &Metadata{}}, ErrNilRenderContext
	}
	if ctx == nil {
		ctx = rc.Context()
	}
	if ctx == nil {
		ctx = context.Background()
	}

	metadata := &Metadata{Page: rc.Page.Name, Locale: rc.Locale}

	root := rc.Page.Root()
	if root == nil {
		// The page's own tags are known even though no fragment ran, and every
		// other failure path reports what it collected; this one does too.
		empty := &renderState{page: rc.Page.Name, tags: make(map[string]struct{})}
		result := &Result{DependencyTags: empty.sortedTags(rc.Page.DependencyTags), Metadata: metadata}
		return result, fmt.Errorf("%w: page %q", ErrNoRootFragment, rc.Page.Name)
	}

	ctx, span := e.tracer.StartSpan(ctx, "collage.render")
	defer span.End()
	span.SetAttribute("page", rc.Page.Name)
	span.SetAttribute("locale", rc.Locale)
	rc = rc.WithContext(ctx)
	e.bindAssets(rc)

	state := &renderState{
		page:       rc.Page.Name,
		tags:       make(map[string]struct{}),
		hoistToken: newHoistToken(),
	}

	start := time.Now()
	html, err := e.renderFragment(rc, root, state, nil)
	// Resolved on the finished tree, so a declaration made anywhere below a marker
	// still reaches it — which is the whole reason a marker is written rather than
	// the content itself.
	html = resolveHoists(html, state.hoistToken, rc.Hoisted())
	total := time.Since(start)

	// Reported whether or not the render succeeded: a render that failed still
	// consumed the time it took, and failures are usually the slow ones. A metrics
	// layer that only sees successful renders is blind to the case that matters.
	e.metrics.RenderDuration(ctx, rc.Page.Name, total, false)

	metadata.Timing = observability.Timing{
		Total:     total,
		Data:      state.dataTime,
		Template:  state.templateTime,
		CacheHit:  false,
		Fragments: len(state.fragments),
	}
	metadata.Fragments = state.fragments

	// What Cached declared belongs to the page too: a page built from a cached
	// value is invalidated with it.
	state.addTags(types.DeclaredTags(rc))
	result := &Result{
		HTML:           html,
		DependencyTags: state.sortedTags(rc.Page.DependencyTags),
		Metadata:       metadata,
		NotFound:       state.notFound,
	}

	if err != nil {
		// Explicit, not incidental: a failed render must never hand back markup,
		// however far through the tree it got before failing.
		result.HTML = nil
		span.RecordError(err)
		return result, err
	}
	return result, nil
}
