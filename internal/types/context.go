package types

import (
	"context"
	"net/http"
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
	SharedData map[string]any // any: fragments exchange arbitrary values
	// ctx is the underlying context for cancellation, deadlines, and request-scoped
	// values. It is unexported; use Context and WithContext to read and derive it.
	ctx context.Context
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
func (rc *RenderContext) Get(key string) (any, bool) { // any: SharedData values are opaque, fragment inter-communication
	v, ok := rc.SharedData[key]
	return v, ok
}

// Set stores value under key in SharedData, initialising the map first if it is nil.
func (rc *RenderContext) Set(key string, value any) { // any: SharedData values are opaque, fragment inter-communication
	if rc.SharedData == nil {
		rc.SharedData = make(map[string]any) // any: fragments exchange arbitrary values
	}
	rc.SharedData[key] = value
}
