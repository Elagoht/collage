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
	// Page is the page being rendered.
	Page *Page
	// SharedData lets fragments exchange arbitrary values within a single render.
	//
	// Read and write it through Get and Set, not directly. Sibling fragments' data
	// handlers run concurrently, so a bare map write from one and a read from
	// another is a data race; Get and Set hold the lock that makes them safe. The
	// field itself remains reachable for what happens after the render, such as
	// AfterRenderEvent.Data, where nothing is running any more.
	SharedData map[string]any // any: fragments exchange arbitrary values
	// ctx is the underlying context for cancellation, deadlines, and request-scoped
	// values. It is unexported; use Context and WithContext to read and derive it.
	ctx context.Context
	// hoisted collects what this render's fragments declare for the page. It is
	// shared by every fragment in one render, which is what WithContext's shallow
	// copy preserves.
	hoisted *Hoisted
	// state is everything one render shares across the fragments running in it:
	// the lock guarding SharedData, and the single-flight table behind Once. It is
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
	mu   sync.Mutex
	once map[string]*onceCall
	// assets resolves a mounted file's content-addressed URL, bound by the
	// render engine; see BindAssets.
	assets func(urlPath string) (string, error)
}

// NewRenderContext builds a RenderContext for one render. It copies params into a
// fresh map rather than aliasing the caller's — mutating the caller's map after
// construction never affects the returned RenderContext — initialises SharedData,
// and defaults a nil ctx to context.Background().
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
		SharedData: make(map[string]any), // any: fragments exchange arbitrary values
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
// PathParams and SharedData are shared with the original by design: the copy is not
// a deep clone, so mutations made through either the copy or the original are
// visible on both.
func (rc *RenderContext) WithContext(ctx context.Context) *RenderContext {
	cp := *rc
	cp.ctx = ctx
	return &cp
}

// Param returns the path parameter named name, or the empty string if absent.
func (rc *RenderContext) Param(name string) string {
	return rc.PathParams[name]
}

// Get returns the value stored under key in SharedData and whether it was present.
//
// It takes the render's lock, because sibling fragments' data handlers run
// concurrently and one of them may be writing.
func (rc *RenderContext) Get(key string) (any, bool) { // any: SharedData values are opaque, fragment inter-communication
	if rc.state == nil {
		v, ok := rc.SharedData[key]
		return v, ok
	}
	rc.state.mu.Lock()
	defer rc.state.mu.Unlock()
	v, ok := rc.SharedData[key]
	return v, ok
}

// Set stores value under key in SharedData, initialising the map first if it is nil.
//
// Note what it does not do: a Get that misses, followed by work, followed by a Set
// is two fragments doing that work twice when they run at the same time. Once is
// the form that does not have that gap.
func (rc *RenderContext) Set(key string, value any) { // any: SharedData values are opaque, fragment inter-communication
	if rc.state == nil {
		if rc.SharedData == nil {
			rc.SharedData = make(map[string]any) // any: fragments exchange arbitrary values
		}
		rc.SharedData[key] = value
		return
	}
	rc.state.mu.Lock()
	defer rc.state.mu.Unlock()
	if rc.SharedData == nil {
		rc.SharedData = make(map[string]any) // any: fragments exchange arbitrary values
	}
	rc.SharedData[key] = value
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
// Everything a render shares — SharedData, the hoist collector, the Once table — is
// reached through pointers the copy keeps, so a fragment's context sees the same
// render as every other fragment's.
func (rc *RenderContext) WithFragment(depth, order int) *RenderContext {
	cp := *rc
	cp.depth = depth
	cp.order = order
	return &cp
}
