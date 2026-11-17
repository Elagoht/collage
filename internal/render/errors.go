package render

import (
	"errors"
	"fmt"
)

// ErrNoRootFragment is returned when the page being rendered has no root fragment:
// neither a layout fragment nor a content fragment.
var ErrNoRootFragment = errors.New("collage: page has no root fragment")

// ErrNilRenderContext is returned when Render is called with a nil RenderContext, or
// with one whose Page is nil. The two share a sentinel deliberately: both mean the
// caller handed the engine nothing to render, and a caller cannot react to them
// differently.
var ErrNilRenderContext = errors.New("collage: nil render context")

// ErrMaxDepthExceeded is returned when a fragment tree nests deeper than
// Options.MaxDepth. The wrapped message names the fragment chain that reached the
// limit, because the usual cause is a fragment bound, directly or indirectly, into
// one of its own slots.
var ErrMaxDepthExceeded = errors.New("collage: max fragment depth exceeded")

// maxRuntimeSources bounds the inline template sources a render may add to the
// template set after registration — the ones a slot resolver's fragments bring.
// A source is Go code and there are as many as the program has literals; a number
// past this is a source built from request data.
const maxRuntimeSources = 256

// ErrTooManyInlineSources is a fragment's failure when its inline Source would be
// one more than maxRuntimeSources the set has taken in while rendering. A source
// must be static: build the template once and vary what it renders with data.
var ErrTooManyInlineSources = errors.New("collage: too many inline template sources added while rendering; a Source must be fixed, not built from request data")

// ErrRequiredSlotEmpty is returned when a fragment declares a slot as Required and
// nothing is bound to it at render time. It is deliberately distinct from
// types.ErrRequiredSlotUnfilled, which reports the same condition from
// Fragment.Validate: that one is a registration-time configuration error the
// embedding application fixes before serving, while this one is a per-render failure
// subject to the fragment failure policy, so the two reach different callers who act
// on them differently.
var ErrRequiredSlotEmpty = errors.New("collage: required slot is empty")

// fatalError marks an error that the per-fragment failure policy must not absorb.
// Without it, "a required fragment's failure fails the whole render" would only hold
// until the error reached an ancestor that happens to be optional and to have a
// fallback: that ancestor would quietly swallow it and the page would render as if
// nothing were wrong. Wrapping keeps the error's own identity intact, so
// errors.Is against the sentinel underneath still works for callers.
type fatalError struct {
	err error
}

// Error returns the wrapped error's message unchanged: fatality is an internal
// routing decision, not something the caller needs to read about.
func (e *fatalError) Error() string { return e.err.Error() }

// Unwrap returns the wrapped error.
func (e *fatalError) Unwrap() error { return e.err }

// fatal marks err as non-absorbable. It is a no-op for an error that is already
// fatal anywhere in its chain, so propagating one up through several frames does not
// stack wrappers.
func fatal(err error) error {
	if err == nil || isFatal(err) {
		return err
	}
	return &fatalError{err: err}
}

// isFatal reports whether err was marked fatal anywhere in its chain. The check must
// be a chain walk rather than a type assertion: a child fragment's fatal error
// surfaces to its parent wrapped in template execution errors.
func isFatal(err error) bool {
	var f *fatalError
	return errors.As(err, &f)
}

// wrapFragment annotates err with the fragment and the stage it came from. It uses
// %w, so both the underlying sentinel and any fatal marking survive.
func wrapFragment(stage, name string, err error) error {
	return fmt.Errorf("collage: fragment %q %s: %w", name, stage, err)
}
