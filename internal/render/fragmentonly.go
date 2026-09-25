package render

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"sort"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

// FragmentResult is one fragment rendered on its own, in parts.
type FragmentResult struct {
	// HTML is the fragment's markup, with any marker it wrote itself resolved.
	HTML []byte
	// Head is what the fragment and its subtree hoisted into areas the fragment
	// placed no marker for — the declarations that, inside the page, would have
	// landed in the layout. In the order the page would have written them.
	Head []types.HoistItem
	// DependencyTags are the tags the render depended on: those the data handlers
	// returned and those Cached declared, sorted and deduplicated.
	DependencyTags []string
}

// Body is the response a request for the fragment is answered with: the markup,
// after a <template data-collage-hoist> element per item of Head.
//
// A fragment answered on its own has no layout, so what it hoisted would otherwise
// be lost: a stylesheet it asks for with {{stylesheet}}, in a fragment the page did
// not render the first time, would never load. The template elements carry it to
// the client instead, each naming its area and its key — the key the page's own
// head deduplicated by — so a script can add to the document's head what it does
// not already have. A <template> is inert: a client that ignores the channel
// inserts elements that render nothing and run nothing.
func (r *FragmentResult) Body() []byte {
	if len(r.Head) == 0 {
		return r.HTML
	}
	var out bytes.Buffer
	for _, item := range r.Head {
		out.WriteString(`<template data-collage-hoist="`)
		out.WriteString(html.EscapeString(item.Area))
		out.WriteString(`" data-collage-key="`)
		out.WriteString(html.EscapeString(item.Key))
		out.WriteString(`">`)
		out.WriteString(string(item.HTML))
		out.WriteString(`</template>`)
	}
	out.Write(r.HTML)
	return out.Bytes()
}

// RenderFragment renders one fragment and its subtree on its own, with no page
// around it, and returns the response body: RenderFragmentResult's Body.
func (e *SlotEngine) RenderFragment(ctx context.Context, rc *types.RenderContext, f *types.Fragment) ([]byte, error) {
	result, err := e.RenderFragmentResult(ctx, rc, f)
	if err != nil {
		return nil, err
	}
	return result.Body(), nil
}

// RenderFragmentResult renders one fragment and its subtree on its own, with no
// page around it.
//
// It is how a part of a page is answered separately from the page: a search result
// list refreshed without the header and the sidebar, the row a form just created, a
// panel a fetch() asked for. The fragment renders exactly as it would inside its
// page — its data handler runs, its children are prefetched and rendered, its
// failure policy applies — because it is the same walk, started lower down.
//
// A marker the fragment writes itself is filled as in a page. What was hoisted
// into an area with no marker here is returned as Head rather than dropped: inside
// the page it would have reached the layout, and the client is the only one left
// who can put it there.
//
// The result is never cached by the framework. A fragment answered on its own is
// usually answering a question about right now.
func (e *SlotEngine) RenderFragmentResult(ctx context.Context, rc *types.RenderContext, f *types.Fragment) (*FragmentResult, error) {
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
	e.bindAssets(rc)

	state := &renderState{
		page:       name,
		tags:       make(map[string]struct{}),
		hoistToken: newHoistToken(),
	}

	start := time.Now()
	markup, err := e.renderFragment(rc, f, state, nil)
	placed := markedAreas(markup, state.hoistToken)
	markup = resolveHoists(markup, state.hoistToken, rc.Hoisted())
	total := time.Since(start)

	e.metrics.RenderDuration(ctx, name, total, false)

	if err != nil {
		span.RecordError(err)
		// Same rule as a page's: a failed render hands back no markup, however far
		// through the subtree it got.
		return nil, err
	}

	result := &FragmentResult{HTML: markup}
	hoisted := rc.Hoisted()
	areas := hoisted.Areas()
	sort.Strings(areas)
	for _, area := range areas {
		if !placed[area] {
			result.Head = append(result.Head, hoisted.Items(area)...)
		}
	}
	state.addTags(types.DeclaredTags(rc))
	result.DependencyTags = state.sortedTags(nil)
	return result, nil
}
