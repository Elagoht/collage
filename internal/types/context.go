package types

import (
	"context"
	"html/template"
	"net/http"
	"sync"
)

// RenderContext carries request-scoped state to every fragment in one render. It is
// request-scoped and MUST NOT be retained past the render that created it — hold no
// reference to a RenderContext once the render it belongs to has finished.
type RenderContext struct {
	// Request is the inbound HTTP request being rendered.
	Request *http.Request
	// Locale is the resolved locale for this render.
	Locale string
	// PathParams holds the path parameters extracted from the matched route.
	PathParams map[string]string
	// Page is the page being rendered. In an action's handler it is the page whose
	// URL the action answers on — the page a form posted to, which is what a
	// validation failure answers with — and nil for an action at a URL of its own.
	Page *Page
	// values are what this render's fragments share through Key.Set and
	// Key.Get. A pointer, so the copies WithContext and WithFragment make all
	// reach the same ones.
	values *Values
	// ctx is the underlying context for cancellation, deadlines, and request-scoped
	// values. It is unexported; use Context and WithContext to read and derive it.
	ctx context.Context
	// hoisted collects what this render's fragments declare for the page. It is
	// shared by every fragment in one render, which is what WithContext's shallow
	// copy preserves.
	hoisted *Hoisted
	// state is everything else one render shares across the fragments running in
	// it: the single-flight table behind Once and what collage binds. It is
	// a pointer so the shallow copies WithContext and WithFragment make all reach
	// the same one — and so that copying a RenderContext never copies a mutex.
	state *renderShared
	// depth is how deep in the fragment tree this context's fragment sits, and
	// order is the number it was launched with. Hoist carries both, which is what
	// lets the innermost declaration win without the collector having to track a
	// "current" position that concurrent handlers would make meaningless.
	depth int
	order int
}

// renderShared is the state one render's fragments share. Its zero value is not
// usable; NewRenderContext builds it.
type renderShared struct {
	// mu guards the Once table and the fields after it.
	mu   sync.Mutex
	once map[string]*onceCall
	// assets resolves a mounted file's content-addressed URL, bound by the
	// render engine; see BindAssets.
	assets func(urlPath string) (string, error)
	// routes builds page and action URLs, bound by collage to every render and
	// action it runs; see BindRoutes.
	routes *Routes
	// data is the application's cross-render data cache, bound by the render
	// engine; nil when there is none. See Cached.
	data DataCache
	// uncached is set for a request that skips the caches — a preview — so
	// Cached fetches fresh rather than serving or storing a value.
	uncached bool
	// tags are the dependency tags declared through Cached, merged into the
	// render's own when it finishes.
	tags []string
}

// NewRenderContext builds a RenderContext for one render. It copies params into a
// fresh map rather than aliasing the caller's — mutating the caller's map after
// construction never affects the returned RenderContext — gives the render its
// values, and defaults a nil ctx to context.Background().
func NewRenderContext(ctx context.Context, req *http.Request, page *Page, locale string, params map[string]string) *RenderContext {
	if ctx == nil {
		ctx = context.Background()
	}
	copied := make(map[string]string, len(params))
	for k, v := range params {
		copied[k] = v
	}
	return &RenderContext{
		Request:    req,
		Locale:     locale,
		PathParams: copied,
		Page:       page,
		values:     &Values{},
		ctx:        ctx,
		hoisted:    NewHoisted(),
		state:      &renderShared{once: make(map[string]*onceCall)},
	}
}

// Context returns rc's underlying context.Context.
func (rc *RenderContext) Context() context.Context {
	return rc.ctx
}

// WithContext returns a shallow copy of rc with ctx replacing the underlying
// context — used to give an individual fragment a derived, per-fragment timeout.
// PathParams and the render's values are shared with the original by design: the
// copy is not a deep clone, so mutations made through either the copy or the
// original are visible on both.
func (rc *RenderContext) WithContext(ctx context.Context) *RenderContext {
	cp := *rc
	cp.ctx = ctx
	return &cp
}

// Param returns the path parameter named name, or the empty string if absent.
func (rc *RenderContext) Param(name string) string {
	return rc.PathParams[name]
}

// Hoist declares html for the page's area, under key.
//
// It is how a fragment contributes something that belongs to the page rather than
// to itself — a stylesheet, a title, a preload hint — without knowing where in the
// document it will land. The layout decides that with {{hoist "area"}}.
//
//	rc.Hoist("head", "css:/static/gallery.css",
//		`<link rel="stylesheet" href="/static/gallery.css">`)
//
// The key is what makes one declaration the same as another. Distinct keys all
// appear, in the order they were first declared; the same key declared twice keeps
// the innermost one, because that is what specificity looks like in a fragment
// tree. A layout naming a default title and an article naming its own are not in
// conflict — the article is more specific, and wins.
//
// html is inserted without escaping, exactly as the caller wrote it. That is the
// point of the mechanism and the responsibility that comes with it: build it from
// values you control, or escape them yourself.
//
// Two fragments at the same depth declaring one key is a conflict with nothing to
// settle it but a rule: the later-declared fragment wins. Declaration order, not
// the order the handlers happened to finish in — sibling handlers run at the same
// time, so finishing order is not something to build a page on.
//
// Call it from a data handler, synchronously, on the handler's own goroutine. A
// handler that hoists from a goroutine it started itself is declaring from
// somewhere with no position in the tree.
func (rc *RenderContext) Hoist(area, key string, html template.HTML) {
	rc.hoisted.Add(area, key, rc.depth, rc.order, html)
}

// Hoisted returns this render's collector. It is how the render engine reads what
// was declared; an application uses Hoist.
func (rc *RenderContext) Hoisted() *Hoisted { return rc.hoisted }

// WithFragment returns a shallow copy of rc positioned at one fragment: depth is how
// deep it sits in the tree and order is the number it was launched with. The render
// engine calls it as it walks; an application has no reason to.
//
// Everything a render shares — the render's values, the hoist collector, the Once table — is
// reached through pointers the copy keeps, so a fragment's context sees the same
// render as every other fragment's.
func (rc *RenderContext) WithFragment(depth, order int) *RenderContext {
	cp := *rc
	cp.depth = depth
	cp.order = order
	return &cp
}
