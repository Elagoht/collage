package router

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/Elagoht/collage/internal/types"
)

// ErrNoMethods reports an action that answers nothing.
var ErrNoMethods = fmt.Errorf("collage: action declares no methods")

// RegisterAction implements Router.
//
// An action shares a tree node with whatever else claims its path, which is the
// point: a page and the POST its form submits are one URL. What must not collide is
// a method — two actions both claiming POST for one path is a coin toss, and the
// node's method map is where that is caught.
func (rt *router) RegisterAction(action *types.Action) error {
	if len(action.Methods) == 0 {
		return fmt.Errorf("%w: action %q", ErrNoMethods, action.Name)
	}
	for _, method := range action.Methods {
		if method != strings.ToUpper(method) {
			return fmt.Errorf("collage: action %q: method %q must be upper case", action.Name, method)
		}
	}

	for _, locale := range actionLocales(action) {
		pattern := action.Paths[locale]

		segments, err := parsePattern(pattern)
		if err != nil {
			return fmt.Errorf("collage: action %q: %w", action.Name, err)
		}

		normalized := normalizePattern(pattern)
		owner := fmt.Sprintf("action %q", action.Name)
		if err := rt.checkRedirectShadow(owner, pattern, locale, normalized); err != nil {
			return err
		}

		tree, ok := rt.routeTrees[locale]
		if !ok {
			tree = &node{}
			rt.routeTrees[locale] = tree
		}
		target, err := tree.insert(segments)
		if err != nil {
			return fmt.Errorf("collage: action %q path %q locale %q: %w", action.Name, pattern, locale, err)
		}

		if target.actions == nil {
			target.actions = make(map[string]*types.Action)
		}
		for _, method := range action.Methods {
			if existing, taken := target.actions[method]; taken {
				return fmt.Errorf("%w: action %q and action %q both claim %s %q for locale %q",
					ErrDuplicateRoute, action.Name, existing.Name, method, pattern, locale)
			}
			target.actions[method] = action
		}

		// Recorded only when nothing else already claims this path. A page and its
		// own form's POST are one URL by design, and reporting that as a duplicate
		// claim would make the ordinary case an error.
		if _, claimed := rt.routedPathsByLocale[locale][normalized]; !claimed {
			rt.recordRoutedPath(locale, pattern, normalized, owner)
		}
	}

	return nil
}

// actionLocales returns the locales an action declares a path for, in sorted order so
// registration errors name the same locale on every run.
func actionLocales(action *types.Action) []string {
	locales := make([]string, 0, len(action.Paths))
	for locale := range action.Paths {
		locales = append(locales, locale)
	}
	slices.Sort(locales)
	return locales
}

// allowedMethods lists every method a node answers, sorted, with the ones a page or
// document implies and the OPTIONS every path answers.
//
// It is what a 405's Allow header carries and what an OPTIONS request is answered
// with — the same list from the same place, so the two cannot disagree about what a
// URL accepts.
func allowedMethods(n *node) []string {
	allowed := map[string]struct{}{http.MethodOptions: {}}
	if n.page != nil || n.document != nil {
		allowed[http.MethodGet] = struct{}{}
		allowed[http.MethodHead] = struct{}{}
	}
	for method := range n.actions {
		allowed[method] = struct{}{}
		// A handler that answers GET answers HEAD: the response is the same one
		// with the body dropped, and net/http does the dropping.
		if method == http.MethodGet {
			allowed[http.MethodHead] = struct{}{}
		}
	}

	out := make([]string, 0, len(allowed))
	for method := range allowed {
		out = append(out, method)
	}
	slices.Sort(out)
	return out
}

// resolve decides what answers a request for this node with this method.
//
// An action wins over a page for the method it declares, and a page answers GET and
// HEAD when no action claims them. Anything else is refused rather than rendered: a
// POST that quietly renders a page is a form submission the application never saw,
// and the reader gets a page that looks like nothing happened.
func resolve(n *node, method string) (page *types.Page, doc *types.Document, action *types.Action, allowed bool) {
	if a, ok := n.actions[method]; ok {
		return nil, nil, a, true
	}
	if method == http.MethodHead {
		if a, ok := n.actions[http.MethodGet]; ok {
			return nil, nil, a, true
		}
	}
	if (method == http.MethodGet || method == http.MethodHead) && (n.page != nil || n.document != nil) {
		return n.page, n.document, nil, true
	}
	return nil, nil, nil, false
}
