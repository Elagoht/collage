package render

import (
	"context"
	"fmt"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

// RenderFragment renders one fragment and its subtree on its own, with no page
// around it, and returns the markup.
//
// It is how a part of a page is answered separately from the page: a search result
// list refreshed without the header and the sidebar, the row a form just created, a
// panel a fetch() asked for. The fragment renders exactly as it would inside its
// page — its data handler runs, its children are prefetched and rendered, its
// failure policy applies — because it is the same walk, started lower down.
//
// Hoisting is resolved against this fragment alone. A fragment rendered on its own
// has no layout to contribute a <head> to, so a declaration made below it has
// nowhere to land unless the fragment itself writes the marker. That is the honest
// behaviour: what a fragment hoists belongs to a page, and there is no page here.
//
// The result is never cached by the framework. A fragment answered on its own is
// usually answering a question about right now.
func (e *SlotEngine) RenderFragment(ctx context.Context, rc *types.RenderContext, f *types.Fragment) ([]byte, error) {
	if rc == nil {
		return nil, ErrNilRenderContext
	}
	if f == nil {
		return nil, fmt.Errorf("%w: nothing to render", types.ErrNilFragment)
	}
	if ctx == nil {
		ctx = rc.Context()
	}
	if ctx == nil {
		ctx = context.Background()
	}

	name := f.Name
	if rc.Page != nil {
		name = rc.Page.Name + "/" + f.Name
	}

	ctx, span := e.tracer.StartSpan(ctx, "collage.render.fragment")
	defer span.End()
	span.SetAttribute("fragment", f.Name)
	span.SetAttribute("locale", rc.Locale)
	rc = rc.WithContext(ctx)

	state := &renderState{
		page:       name,
		tags:       make(map[string]struct{}),
		hoistToken: newHoistToken(),
	}

	start := time.Now()
	html, err := e.renderFragment(rc, f, state, nil)
	html = resolveHoists(html, state.hoistToken, rc.Hoisted())
	total := time.Since(start)

	e.metrics.RenderDuration(ctx, name, total, false)

	if err != nil {
		span.RecordError(err)
		// Same rule as a page's: a failed render hands back no markup, however far
		// through the subtree it got.
		return nil, err
	}
	return html, nil
}
