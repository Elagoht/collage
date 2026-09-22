package render

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// TestRender_RequiredFragmentNotFoundSetsResult pins the core classification: a
// required fragment's data handler returning an error wrapping types.ErrNotFound
// still fails the render — this is a classification of the failure, not a new
// success path — but the Result it fails with reports NotFound so the caller can
// choose a 404 over a 500.
func TestRender_RequiredFragmentNotFoundSetsResult(t *testing.T) {
	engine := newEngine(t, Options{})

	content := fragment("content", "leaf.html")
	content.Required = true
	notFoundErr := fmt.Errorf("blog: slug %q: %w", "no-such-slug", types.ErrNotFound)
	content.DataHandler = failingHandler(notFoundErr)

	result, err := renderPage(t, engine, pageWith(content))

	if err == nil {
		t.Fatal("Render() error = nil, want the required fragment's failure to propagate")
	}
	if !errors.Is(err, types.ErrNotFound) {
		t.Errorf("Render() error = %v, want it to wrap types.ErrNotFound", err)
	}
	if result == nil {
		t.Fatal("Render() result = nil, want a non-nil Result on every path")
	}
	if result.HTML != nil {
		t.Errorf("Render() HTML = %q, want nil alongside an error", result.HTML)
	}
	if !result.NotFound {
		t.Error("Result.NotFound = false, want true for a required fragment's ErrNotFound")
	}
}

// TestRender_NonRequiredFragmentNotFoundDoesNotSetResult pins the other half of the
// rule: a missing sidebar does not make the page missing. A non-required fragment's
// ErrNotFound follows the ordinary optional-failure policy (here: no fallback, so it
// emits nothing) and the page still renders successfully, uncontaminated by NotFound.
func TestRender_NonRequiredFragmentNotFoundDoesNotSetResult(t *testing.T) {
	engine := newEngine(t, Options{})

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
	sidebar := fragment("sidebar", "leaf.html")
	notFoundErr := fmt.Errorf("sidebar: widget: %w", types.ErrNotFound)
	sidebar.DataHandler = failingHandler(notFoundErr)
	bind(t, layout, "content", sidebar)

	result, err := renderPage(t, engine, pageWith(layout))

	if err != nil {
		t.Fatalf("Render() error = %v, want the optional failure to be contained", err)
	}
	if result.NotFound {
		t.Error("Result.NotFound = true, want false: a non-required fragment must not set it")
	}
	if !result.Degraded() {
		t.Error("Degraded() = false, want true: the fragment did fail")
	}
	meta := fragmentMetadata(t, result, "sidebar")
	if !meta.Failed {
		t.Error("FragmentMetadata.Failed = false, want true")
	}
	if !errors.Is(meta.Err, types.ErrNotFound) {
		t.Errorf("FragmentMetadata.Err = %v, want it to wrap types.ErrNotFound", meta.Err)
	}
}

// TestRender_OptionalAncestorFallbackCannotAbsorbNotFound pins that ErrNotFound is
// non-absorbable in exactly the way a required fragment's ordinary error already is:
// an optional ancestor's fallback exists to contain the ancestor's own failure, not to
// swallow a required descendant's fatal error and silently turn a 404 into a degraded
// 200. The required child here is bound into the ancestor's primary slot, not into the
// ancestor's fallback's slot, so this exercises escalation past a fallback rather than
// the (deliberately different) case of a required fragment failing inside a fallback.
func TestRender_OptionalAncestorFallbackCannotAbsorbNotFound(t *testing.T) {
	engine := newEngine(t, Options{})

	ancestor := declare(fragment("ancestor", "layout.html"), &types.SlotDefinition{Name: "content"})
	ancestor.Fallback = fragment("ancestor-fallback", "fallback.html")

	child := fragment("child", "leaf.html")
	child.Required = true
	notFoundErr := fmt.Errorf("blog: slug %q: %w", "no-such-slug", types.ErrNotFound)
	child.DataHandler = failingHandler(notFoundErr)
	bind(t, ancestor, "content", child)

	result, err := renderPage(t, engine, pageWith(ancestor))

	if err == nil {
		t.Fatal("Render() error = nil, want the required descendant's ErrNotFound to escalate past the optional ancestor's fallback")
	}
	if !errors.Is(err, types.ErrNotFound) {
		t.Errorf("Render() error = %v, want it to wrap types.ErrNotFound", err)
	}
	if result.HTML != nil {
		t.Errorf("Render() HTML = %q, want nil: the ancestor's fallback must not have rendered", result.HTML)
	}
	if !result.NotFound {
		t.Error("Result.NotFound = false, want true: the ancestor's fallback must not absorb it")
	}
	meta := fragmentMetadata(t, result, "ancestor")
	if meta.UsedFallback {
		t.Error("FragmentMetadata.UsedFallback = true, want false: a fatal error must skip the fallback entirely")
	}
}

// TestRender_OrdinaryErrorDoesNotSetResultNotFound confirms the classification is
// exact: only errors.Is(err, types.ErrNotFound) sets NotFound, never an ordinary
// error, even from a required fragment where the error is just as fatal to the
// render.
func TestRender_OrdinaryErrorDoesNotSetResultNotFound(t *testing.T) {
	engine := newEngine(t, Options{})

	content := fragment("content", "leaf.html")
	content.Required = true
	boom := errors.New("collage: data source down")
	content.DataHandler = failingHandler(boom)

	result, err := renderPage(t, engine, pageWith(content))

	if err == nil {
		t.Fatal("Render() error = nil, want the required fragment's failure to propagate")
	}
	if errors.Is(err, types.ErrNotFound) {
		t.Error("Render() error unexpectedly wraps types.ErrNotFound")
	}
	if result.NotFound {
		t.Error("Result.NotFound = true, want false: an ordinary error must not be classified as not-found")
	}
}
