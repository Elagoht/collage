package render

import (
	"context"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

// prefetch is one fragment's data handler, started before the fragment itself is
// reached.
//
// Fragments render depth-first and in order, which means a page made of a navigation
// bar, an article and a popular-posts sidebar makes its three upstream calls one
// after another and waits for the sum. None of the three needs anything from the
// others; they are simply siblings, and the tree already says so.
//
// So a fragment starts its declared children's data handlers before executing its
// own template, and the children collect results that are already on their way.
// Parent before child is preserved exactly as it was — a child's handler starts only
// after its parent's has returned — so a parent that puts something in SharedData
// for its children to read still works. What now overlaps is siblings, which is
// where the waiting actually was.
//
// The speculation this allows is real and bounded: a template may not render a slot
// it declares, and that fragment's handler will have run for nothing. It is one
// level of lookahead, its context is cancelled as soon as the parent's template is
// done with it, and its failure is discarded — a fragment that was never rendered
// cannot fail a page.
type prefetch struct {
	// done closes when the handler has finished.
	done chan struct{}
	// cancel stops the handler when nothing will consume it.
	cancel context.CancelFunc
	// depth and order are the position the fragment was launched at, so what it
	// hoists is placed as if the walk had reached it in order.
	depth int
	order int

	data    any // any: fragment data is opaque to the framework, exactly as in attempt
	tags    []string
	err     error
	elapsed time.Duration
}

// wait blocks until the handler has finished and returns what it produced.
func (p *prefetch) wait() (any, []string, time.Duration, error) { // any: see the data field
	<-p.done
	return p.data, p.tags, p.elapsed, p.err
}

// prefetchChildren starts the data handlers of every fragment bound into f's slots.
//
// The launch happens here, on the render's own goroutine, in the order the tree
// declares: that is what keeps depth and order deterministic, and therefore what
// keeps hoisting deterministic, even though the handlers themselves finish in
// whatever order they finish.
//
// A fragment with no data handler is not launched. There is nothing to overlap, and
// a prefetch entry for it would only add a channel to wait on.
func (e *SlotEngine) prefetchChildren(rc *types.RenderContext, f *types.Fragment, state *renderState, fills slotFills) map[*types.Fragment][]*prefetch {
	// One level deeper than the fragment starting them: if that is past the limit,
	// the render is going to fail there anyway and starting upstream calls for a
	// tree that cannot render is pure waste.
	if len(state.stack)+1 > e.maxDepth {
		return nil
	}

	var started map[*types.Fragment][]*prefetch
	for _, name := range f.SlotNames() {
		slot, ok := f.Slot(name)
		if !ok || slot == nil {
			continue
		}
		for _, child := range fills.of(slot) {
			if child == nil || child.DataHandler == nil {
				continue
			}
			if started == nil {
				started = make(map[*types.Fragment][]*prefetch)
			}
			started[child] = append(started[child], e.startPrefetch(rc, child, state))
		}
	}
	return started
}

// startPrefetch runs one fragment's data handler in the background.
func (e *SlotEngine) startPrefetch(rc *types.RenderContext, f *types.Fragment, state *renderState) *prefetch {
	ctx, cancel := context.WithCancel(rc.Context())

	p := &prefetch{
		done:   make(chan struct{}),
		cancel: cancel,
		depth:  len(state.stack) + 1,
		order:  state.nextOrder(),
	}

	// The handler's context carries the fragment's own position, so a handler that
	// hoists is placed where the fragment is rather than where the walk happens to
	// be — there is no longer one place the walk "is".
	handlerRC := rc.WithContext(ctx).WithFragment(p.depth, p.order)

	// Before the handler starts, for the reason attempt gives.
	if f.Title != "" {
		handlerRC.HoistTitle(f.Title)
	}

	go func() {
		defer close(p.done)
		started := time.Now()
		p.err = Execute(ctx, f.EffectiveTimeout(e.defaultTimeout), func(ctx context.Context) error {
			var handlerErr error
			p.data, p.tags, handlerErr = f.DataHandler(ctx, handlerRC.WithContext(ctx))
			return handlerErr
		})
		p.elapsed = time.Since(started)
	}()

	return p
}

// release cancels every prefetch that nothing took.
//
// Called once the parent's template has finished: whatever it was going to render,
// it has rendered, so a handler still running is producing something no one will
// look at. Cancelling is not merely tidy — it stops an upstream call that a
// conditional template decided against.
func release(started map[*types.Fragment][]*prefetch) {
	for _, list := range started {
		for _, p := range list {
			if p != nil {
				p.cancel()
			}
		}
	}
}
