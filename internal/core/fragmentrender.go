package core

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"

	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/types"
)

// ErrNoRequest is returned by RenderFragment when it is given no request to render
// for.
var ErrNoRequest = errors.New("collage: no request to render for")

// RenderFragment renders one fragment a page opened at its own URL with
// WithFragmentPath, for r, and returns it in parts: the markup, what it hoisted,
// the tags it depended on, and whether one render can serve every reader.
//
// It is the render a request to the fragment's path gets, without the response: a
// plugin pushing fragments over an event stream or a WebSocket calls it for each
// fragment an invalidation touched, and sends the parts in a message of its own.
// Only a fragment the page opened is rendered — the same set of fragments HTTP
// reaches — so a stream cannot be used to read a part of a page nobody published.
//
// Plugins reach it through Host.RenderFragment.
func (a *App) RenderFragment(r *http.Request, req plugin.FragmentRequest) (*plugin.FragmentRender, error) {
	if r == nil {
		return nil, ErrNoRequest
	}
	locale := req.Locale
	if locale == "" {
		locale = a.cfg.Locale.Default
	}
	if !a.localeReachable(locale) {
		return nil, fmt.Errorf("%w: %q", ErrLocaleUnreachable, locale)
	}

	a.mu.RLock()
	page := a.pages[req.Page]
	a.mu.RUnlock()
	if page == nil {
		return nil, fmt.Errorf("%w: %q", types.ErrUnknownRoute, req.Page)
	}
	var fragment *types.Fragment
	for _, f := range page.PathFragments() {
		if f.Name == req.Fragment {
			fragment = f
			break
		}
	}
	if fragment == nil {
		return nil, fmt.Errorf("%w: %q on page %q", types.ErrUnknownFragmentPath, req.Fragment, req.Page)
	}

	renderer, ok := a.renderer.(render.FragmentResultRenderer)
	if !ok {
		return nil, fmt.Errorf("collage: this application's renderer cannot render a fragment in parts")
	}
	rc := types.NewRenderContext(r.Context(), r, page, locale, req.Params)
	result, err := renderer.RenderFragmentResult(r.Context(), rc, fragment)
	if err != nil {
		return nil, err
	}

	out := &plugin.FragmentRender{
		HTML:           result.HTML,
		Head:           result.Head,
		DependencyTags: result.DependencyTags,
		// A page served from one cached render to every reader has said its
		// handlers answer the same for everyone; a fragment of it can say no less.
		Shared: page.Strategy.Cacheable() || fetchFree(fragment),
	}
	if a.csrf != nil {
		if marker := []byte(a.csrf.Marker()); bytes.Contains(out.HTML, marker) {
			token, minted, err := a.csrf.TokenFor(r)
			if err != nil {
				return nil, fmt.Errorf("collage: could not issue a forgery token: %w", err)
			}
			out.HTML = bytes.ReplaceAll(out.HTML, marker, []byte(token))
			out.Shared = false
			if minted {
				out.Cookie = a.csrf.Cookie(r, token)
			}
		}
	}
	return out, nil
}

// fetchFree reports whether nothing in f's subtree renders differently per
// request: no data handler that is not declared Static, no slot resolver. The same
// test resolveStrategy makes of a whole page, made of one fragment.
func fetchFree(f *types.Fragment) bool {
	fetches := errors.New("fetches per render")
	err := walkFragments(f, make(map[*types.Fragment]bool), func(f *types.Fragment) error {
		if f.DataHandler != nil && !f.Static {
			return fetches
		}
		for _, slot := range f.Slots {
			if slot != nil && slot.Resolve != nil {
				return fetches
			}
		}
		return nil
	})
	return err == nil
}
