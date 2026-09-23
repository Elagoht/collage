package render

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

// gate lets a handler block until a given number of handlers have arrived. If the
// handlers run one after another, the first one waits for a number that can never be
// reached, and the test fails on the deadline rather than passing slowly.
type gate struct {
	mu      sync.Mutex
	cond    *sync.Cond
	arrived int
}

func newGate() *gate {
	g := &gate{}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// arrive blocks until n handlers have called it, and reports whether they did before
// the deadline.
func (g *gate) arrive(n int, within time.Duration) bool {
	g.mu.Lock()
	g.arrived++
	g.cond.Broadcast()
	g.mu.Unlock()

	deadline := time.Now().Add(within)
	g.mu.Lock()
	defer g.mu.Unlock()
	for g.arrived < n {
		if time.Now().After(deadline) {
			return false
		}
		g.mu.Unlock()
		time.Sleep(time.Millisecond)
		g.mu.Lock()
	}
	return true
}

// TestPrefetch_SiblingHandlersRunAtTheSameTime is the whole point of prefetching: a
// page made of three fragments that need nothing from each other should make its
// three upstream calls at once rather than waiting for the sum.
//
// It is written as a rendezvous rather than as a timing comparison. Three handlers
// each wait for all three to arrive; sequential handlers can never satisfy that, so
// the test fails outright instead of being slow on a loaded machine.
func TestPrefetch_SiblingHandlersRunAtTheSameTime(t *testing.T) {
	engine := newEngine(t, Options{})
	g := newGate()
	var together atomic.Bool
	together.Store(true)

	handler := func(context.Context, *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		if !g.arrive(3, 5*time.Second) {
			together.Store(false)
		}
		return nil, nil, nil
	}

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content", AllowMultiple: true})
	for _, name := range []string{"one", "two", "three"} {
		child := fragment(name, "leaf.html")
		child.DataHandler = handler
		bind(t, layout, "content", child)
	}

	if _, err := renderPage(t, engine, pageWith(layout)); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !together.Load() {
		t.Error("sibling data handlers did not overlap; each waited for the one before it")
	}
}

// A child's handler must not start before its parent's has returned. Fragments talk
// to each other through SharedData, and a parent putting something there for its
// children to read is the ordinary way to do it.
func TestPrefetch_ParentRunsBeforeItsChild(t *testing.T) {
	engine := newEngine(t, Options{})

	parent := declare(fragment("parent", "section.html"), &types.SlotDefinition{Name: "inner"})
	parent.DataHandler = func(_ context.Context, rc *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		rc.Set("from-parent", "value")
		return nil, nil, nil
	}

	var seen atomic.Value
	child := fragment("child", "leaf.html")
	child.DataHandler = func(_ context.Context, rc *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		v, _ := rc.Get("from-parent")
		seen.Store(v == "value")
		return nil, nil, nil
	}
	bind(t, parent, "inner", child)

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
	bind(t, layout, "content", parent)

	if _, err := renderPage(t, engine, pageWith(layout)); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if ok, _ := seen.Load().(bool); !ok {
		t.Error("the child did not see what its parent shared; parent-before-child was not preserved")
	}
}

// A fragment whose slot the template never renders must not fail the page, even when
// it is Required and even when its prefetched handler failed. Requiredness is about
// a fragment the page renders, and prefetching must not turn a slot nobody asked for
// into a reason to fail.
func TestPrefetch_UnrenderedFragmentCannotFailThePage(t *testing.T) {
	engine := newEngine(t, Options{})

	// plain.html renders no slot at all.
	parent := declare(fragment("parent", "plain.html"), &types.SlotDefinition{Name: "inner"})
	child := fragment("child", "leaf.html")
	child.Required = true
	child.DataHandler = failingHandler(errors.New("upstream is down"))
	bind(t, parent, "inner", child)

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
	bind(t, layout, "content", parent)

	result, err := renderPage(t, engine, pageWith(layout))
	if err != nil {
		t.Fatalf("Render() error = %v, want nil: a slot the template never rendered cannot fail the page", err)
	}
	if result.Degraded() {
		t.Error("Degraded() = true for a fragment that was never rendered")
	}
}

// A fragment bound twice gets two handler runs, not one shared result. Two bindings
// of one fragment are two fragments as far as a page is concerned.
func TestPrefetch_OneFragmentBoundTwiceRunsTwice(t *testing.T) {
	engine := newEngine(t, Options{})
	var calls atomic.Int64

	child := fragment("child", "leaf.html")
	child.DataHandler = func(context.Context, *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		calls.Add(1)
		return nil, nil, nil
	}

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content", AllowMultiple: true})
	bind(t, layout, "content", child, child)

	if _, err := renderPage(t, engine, pageWith(layout)); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("handler calls = %d, want 2", got)
	}
}

// A failing prefetched handler fails its fragment exactly as a sequential one did.
func TestPrefetch_HandlerFailureStillFailsItsFragment(t *testing.T) {
	engine := newEngine(t, Options{})
	sentinel := errors.New("upstream is down")

	child := fragment("child", "leaf.html")
	child.Required = true
	child.DataHandler = failingHandler(sentinel)

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
	bind(t, layout, "content", child)

	_, err := renderPage(t, engine, pageWith(layout))
	if !errors.Is(err, sentinel) {
		t.Fatalf("Render() error = %v, want it to wrap the handler's error", err)
	}
}

// Tags a prefetched handler reported still reach the render's tag set, which is what
// a page's cache entry is invalidated by.
func TestPrefetch_TagsSurvive(t *testing.T) {
	engine := newEngine(t, Options{})

	child := fragment("child", "leaf.html")
	child.DataHandler = dataHandler(nil, "article:7")

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
	bind(t, layout, "content", child)

	result, err := renderPage(t, engine, pageWith(layout))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	found := false
	for _, tag := range result.DependencyTags {
		if tag == "article:7" {
			found = true
		}
	}
	if !found {
		t.Errorf("DependencyTags = %v, want it to include the prefetched handler's tag", result.DependencyTags)
	}
}

// A handler started for a slot the template never renders is cancelled once the
// template is done, rather than left running against an upstream nobody is waiting
// for. It is the difference between one level of speculation and a request that
// outlives the page it was speculating for.
//
// Two children, in two slots, of which the template renders one. The rendered one's
// handler waits until the unrendered one's has started, which is what removes the
// race: without it, the release can land before the unused goroutine has run at all
// — a perfectly good outcome, since nothing ran, but not the one this test is about.
func TestPrefetch_UnusedHandlerIsCancelled(t *testing.T) {
	engine := newEngine(t, Options{})
	unusedStarted := make(chan struct{})
	cancelled := make(chan struct{})

	// section.html renders {{slot "inner"}} and nothing else.
	parent := declare(fragment("parent", "section.html"), &types.SlotDefinition{Name: "inner"})
	declare(parent, &types.SlotDefinition{Name: "unused"})

	used := fragment("used", "leaf.html")
	used.DataHandler = func(context.Context, *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		<-unusedStarted
		return nil, nil, nil
	}
	bind(t, parent, "inner", used)

	unused := fragment("unused", "leaf.html")
	// Far longer than the test waits, so the only thing that can end this handler
	// is the release. Without it the fragment's own timeout would eventually fire
	// and the test would pass for the wrong reason, slowly.
	unused.Timeout = time.Minute
	unused.DataHandler = func(ctx context.Context, _ *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		close(unusedStarted)
		<-ctx.Done()
		close(cancelled)
		return nil, nil, ctx.Err()
	}
	bind(t, parent, "unused", unused)

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
	bind(t, layout, "content", parent)

	if _, err := renderPage(t, engine, pageWith(layout)); err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Error("a handler for a slot that was never rendered was left running after the render finished")
	}
}

// Hoisting must not depend on which goroutine finished first. Two siblings at the
// same depth declaring one key is settled by declaration order, which is fixed by
// the tree, so the same page produces the same head on every render.
func TestPrefetch_HoistingIsDeterministicAcrossConcurrentSiblings(t *testing.T) {
	engine := newEngine(t, Options{})

	// Both siblings declare the same key at the same depth; the later-declared one
	// must win every time. The first one sleeps so that, without an order that is
	// assigned at launch, the finishing order would be the reverse of the
	// declaration order and the wrong one would win.
	first := fragment("first", "leaf.html")
	first.DataHandler = func(_ context.Context, rc *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		time.Sleep(20 * time.Millisecond)
		rc.Hoist("head", "title", "<title>first</title>")
		return nil, nil, nil
	}
	second := fragment("second", "leaf.html")
	second.DataHandler = func(_ context.Context, rc *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		rc.Hoist("head", "title", "<title>second</title>")
		return nil, nil, nil
	}

	for range 5 {
		layout := declare(fragment("layout", "hoisthead.html"), &types.SlotDefinition{Name: "content", AllowMultiple: true})
		bind(t, layout, "content", first, second)

		result, err := renderPage(t, engine, pageWith(layout))
		if err != nil {
			t.Fatalf("Render() error = %v", err)
		}
		if got := string(result.HTML); !strings.Contains(got, "<title>second</title>") {
			t.Fatalf("head = %q, want the later-declared sibling's title to win", got)
		}
	}
}

// A deeper fragment still beats a shallower one, whichever finishes first.
func TestPrefetch_InnermostStillWinsWhenHandlersOverlap(t *testing.T) {
	engine := newEngine(t, Options{})

	// The deep one is slow, so a rule based on finishing order would pick the
	// shallow one.
	deep := fragment("deep", "leaf.html")
	deep.DataHandler = func(_ context.Context, rc *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		time.Sleep(20 * time.Millisecond)
		rc.Hoist("head", "title", "<title>deep</title>")
		return nil, nil, nil
	}
	middle := declare(fragment("middle", "section.html"), &types.SlotDefinition{Name: "inner"})
	middle.DataHandler = func(_ context.Context, rc *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		rc.Hoist("head", "title", "<title>middle</title>")
		return nil, nil, nil
	}
	bind(t, middle, "inner", deep)

	layout := declare(fragment("layout", "hoisthead.html"), &types.SlotDefinition{Name: "content"})
	bind(t, layout, "content", middle)

	result, err := renderPage(t, engine, pageWith(layout))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got := string(result.HTML); !strings.Contains(got, "<title>deep</title>") {
		t.Fatalf("head = %q, want the innermost declaration", got)
	}
}

// Once collapses the same fetch made by concurrent siblings into one call. The
// Get/fetch/Set shape it replaces cannot: both siblings miss the Get before either
// reaches the Set.
func TestPrefetch_OnceCollapsesTheSameFetch(t *testing.T) {
	engine := newEngine(t, Options{})
	var calls atomic.Int64
	g := newGate()

	handler := func(_ context.Context, rc *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		// Both handlers are inside this call at the same time, which is exactly
		// the window a Get-then-Set leaves open.
		g.arrive(2, 5*time.Second)
		article, err := types.Once(rc, "article:7", func(context.Context) (string, error) {
			calls.Add(1)
			return "the article", nil
		})
		return article, nil, err
	}

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content", AllowMultiple: true})
	for _, name := range []string{"one", "two"} {
		child := fragment(name, "leaf.html")
		child.DataHandler = handler
		bind(t, layout, "content", child)
	}

	if _, err := renderPage(t, engine, pageWith(layout)); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("fetches = %d, want 1: two fragments asking for one article must ask the upstream once", got)
	}
}
