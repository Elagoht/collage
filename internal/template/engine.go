// Package template wraps a Go template engine behind a narrow interface so the
// underlying implementation (currently html/template) can be swapped without
// touching callers. The render engine and app orchestrator depend only on Engine.
package template

import (
	"context"
	"errors"
	"html/template"
	"io"
)

// ErrTemplateRootMissing is returned when an Engine's configured root directory does
// not exist.
var ErrTemplateRootMissing = errors.New("collage: template root missing")

// ErrTemplateEscapesRoot is returned when a resolved template path falls outside the
// configured root directory. This is a security boundary: templates must never be
// loaded from outside the root they were configured with.
var ErrTemplateEscapesRoot = errors.New("collage: template escapes root")

// ErrTemplateNotFound is returned when Render or RenderWithFuncs is called with a
// path that does not match any loaded template.
var ErrTemplateNotFound = errors.New("collage: template not found")

// ErrHoistOutsideRender is returned by the placeholder "hoist" template function
// when it is invoked outside a render that has bound a real implementation. It
// signals a stray {{hoist "area"}} rather than silently writing nothing.
var ErrHoistOutsideRender = errors.New("collage: hoist called outside render")

// ErrAssetOutsideRender is returned by the placeholder "asset" template function
// when a template calls it outside a render that bound the real implementation.
var ErrAssetOutsideRender = errors.New("collage: asset called outside render")

// ErrSlotOutsideRender is returned by the placeholder "slot" template function when
// it is invoked outside a render that has bound a real slot implementation. It
// signals a stray {{slot "name"}} rather than producing a nil-map panic.
var ErrSlotOutsideRender = errors.New("collage: slot called outside render")

// Engine renders a named template with the supplied data.
type Engine interface {
	// Render executes the template at path with data and writes to w.
	Render(ctx context.Context, w io.Writer, path string, data any) error // any: template data
	// Lookup reports whether a template exists.
	Lookup(path string) bool
	// RenderWithFuncs executes the template at path with data, after overlaying funcs
	// onto the engine's FuncMap for this render only. Implementations MUST do this on a
	// clone, never on the shared template set, so concurrent renders cannot see each
	// other's functions. This is how the render engine binds a per-render slot function.
	RenderWithFuncs(ctx context.Context, w io.Writer, path string, data any, funcs template.FuncMap) error // any: template data
	// Reload discards and reparses all templates. Used in dev mode.
	Reload() error
	// Names returns every loaded template path, sorted.
	Names() []string
}
