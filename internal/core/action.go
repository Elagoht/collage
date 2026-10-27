package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/Elagoht/collage/internal/types"
)

// ErrNilAction reports a nil action passed to RegisterAction.
var ErrNilAction = errors.New("collage: nil action")

// ErrDuplicateAction reports two actions registered under one name.
var ErrDuplicateAction = errors.New("collage: duplicate action")

// ErrEmptyActionName reports an action with no name. Every registration error names
// what failed, and an unnamed action leaves nothing to name.
var ErrEmptyActionName = errors.New("collage: action has no name")

// ErrNoActionPaths reports an action with no path to reach it by.
var ErrNoActionPaths = errors.New("collage: action has no paths")

// ErrNoActionHandler reports an action with nothing to run.
var ErrNoActionHandler = types.ErrNoActionHandler

// RegisterAction validates action and adds it to the router.
//
// An action may share a path with a page — that is what an HTML form needs, since a
// form's action is the page it sits on — but not a method with another action.
func (a *App) RegisterAction(action *types.Action) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.started {
		return fmt.Errorf("%w: cannot register action", ErrAppStarted)
	}
	if action == nil {
		return ErrNilAction
	}
	if action.Name == "" {
		return ErrEmptyActionName
	}
	// What the builder recorded is refused whether or not anyone asked the
	// builder, as a page's is: an action that recorded an empty locale reports
	// that, not that it has no paths.
	if err := types.ActionBuildErr(action); err != nil {
		return fmt.Errorf("collage: action %q was built with errors: %w", action.Name, err)
	}
	if len(action.Paths) == 0 {
		return fmt.Errorf("%w: %q", ErrNoActionPaths, action.Name)
	}
	if action.Handler == nil {
		return fmt.Errorf("%w: %q", ErrNoActionHandler, action.Name)
	}
	if existing, taken := a.actions[action.Name]; taken && existing != action {
		return fmt.Errorf("%w: %q", ErrDuplicateAction, action.Name)
	}

	if err := a.routes.RegisterAction(action); err != nil {
		return err
	}
	if a.actions == nil {
		a.actions = make(map[string]*types.Action)
	}
	a.actions[action.Name] = action
	a.actionOrder = append(a.actionOrder, action)
	a.warnSafeOnlyAction(action)
	return nil
}

// safeOnlyActionWarning is what warnSafeOnlyAction logs.
const safeOnlyActionWarning = "collage: this action answers only GET or HEAD, so it runs with no request-forgery check: " +
	"any site can make a reader's browser call it with a link or an image. " +
	"If it changes anything, declare POST (or PUT, PATCH, DELETE) instead"

// warnSafeOnlyAction tells a developer, in development, about an action whose
// methods are all safe ones. A safe method carries no forgery token by design —
// a token in a URL is a token in a log file — so such an action is a state change
// any page on the web can trigger, unless it changes nothing, which an action
// rarely means. A warning, not a refusal: a read that is an action, a JSON
// endpoint, is legitimate.
func (a *App) warnSafeOnlyAction(action *types.Action) {
	if !a.devMode {
		return
	}
	for _, method := range action.Methods {
		if !types.SafeMethod(method) {
			return
		}
	}
	a.logger.Warn(safeOnlyActionWarning, "action", action.Name, "methods", action.Methods)
}

// Actions returns every registered action in registration order, as a copy.
func (a *App) Actions() []*types.Action {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return slices.Clone(a.actionOrder)
}

// registerPageActions adds the actions a page declared on its own paths.
//
// A page-attached action has no paths of its own: it inherits the page's, which is
// what makes "the form posts to the page it is on" the default rather than something
// to wire up. It must be called with a.mu held, from the page registration that owns
// the paths.
func (a *App) registerPageActions(page *types.Page) error {
	for _, action := range page.Actions {
		if action == nil {
			return fmt.Errorf("%w: page %q", ErrNilAction, page.Name)
		}
		// Copied rather than mutated: the caller's action is theirs, and a builder
		// that filled Paths in from a page would make the same action unusable on
		// a second page.
		bound := *action
		bound.Paths = make(map[string]string, len(page.Paths))
		for locale, pattern := range page.Paths {
			bound.Paths[locale] = pattern
		}
		if bound.Name == "" {
			bound.Name = page.Name + ":" + methodList(bound.Methods)
		}
		// Held to the rules RegisterAction applies, so a page's action without a
		// handler is refused here, by the sentinel an application can match,
		// rather than registered and failing on its first request.
		if bound.Handler == nil {
			return fmt.Errorf("%w: %q on page %q", ErrNoActionHandler, bound.Name, page.Name)
		}
		if _, taken := a.actions[bound.Name]; taken {
			return fmt.Errorf("%w: %q on page %q", ErrDuplicateAction, bound.Name, page.Name)
		}
		if err := a.routes.RegisterAction(&bound); err != nil {
			return err
		}
		if a.actions == nil {
			a.actions = make(map[string]*types.Action)
		}
		a.actions[bound.Name] = &bound
		a.actionOrder = append(a.actionOrder, &bound)
	}
	return nil
}

// methodList joins methods for a generated action name.
func methodList(methods []string) string {
	out := ""
	for i, method := range methods {
		if i > 0 {
			out += "+"
		}
		out += method
	}
	return out
}

// registerFragmentPaths adds the URLs a page opened for its own fragments.
//
// Each becomes an action answering GET and HEAD, which is what it is: a request for
// a part of a page, answered by rendering that part — a read, like the page, so HEAD
// is declared rather than left to a fallback actions no longer have. Reusing the action route rather than
// inventing a third kind means one method table, one 405, one OPTIONS answer for
// every URL the framework serves.
func (a *App) registerFragmentPaths(page *types.Page) error {
	for locale, byPattern := range page.FragmentPaths {
		for pattern, fragment := range byPattern {
			if fragment == nil {
				return fmt.Errorf("%w: page %q fragment path %q", ErrNilFragmentPath, page.Name, pattern)
			}
			target := fragment
			action := &types.Action{
				Name:    page.Name + ":" + fragment.Name,
				Paths:   map[string]string{locale: pattern},
				Methods: []string{http.MethodGet, http.MethodHead},
				Handler: func(ctx context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
					// The page's guards and the fragment's own: see
					// fragmentGuard. The decision is validated before it
					// becomes a result: a redirect with no Location would
					// fall through the result's own switch — Location, then
					// Fragment — and render the fragment the guard refused.
					decision, err := fragmentGuard(ctx, rc.Request, page, target)
					if err != nil {
						return nil, err
					}
					if decision != nil {
						// A decision about this reader, which nothing between
						// the server and the next reader may keep.
						header := http.Header{"Cache-Control": []string{"no-store"}}
						return &types.ActionResult{Status: decision.Status, Location: decision.Location, Header: header}, nil
					}
					// A part of the page that opened it, so its render knows
					// that page: withQuery and localeURL link the page, not
					// the bare fragment's URL.
					rc.Page = page
					return &types.ActionResult{Fragment: target}, nil
				},
			}
			if err := a.routes.RegisterAction(action); err != nil {
				return err
			}
			if a.actions == nil {
				a.actions = make(map[string]*types.Action)
			}
			a.actions[action.Name] = action
			a.actionOrder = append(a.actionOrder, action)
			if a.fragmentPaths == nil {
				a.fragmentPaths = make(map[*types.Action]fragmentPath)
			}
			a.fragmentPaths[action] = fragmentPath{page: page, fragment: target}
		}
	}
	return nil
}

// fragmentGuard asks the guards of a fragment path about r, over HTTP and through
// RenderFragment alike: the guards of the page that opened it, outermost layout
// first, then the fragment's own. A nil decision allows. A decision comes back
// validated, so no caller can turn a redirect with nowhere to go into the render
// the guard refused.
//
// The page's too, because a fragment path is a part of the page, and a part of a
// page a reader may not see is a part they may not see either. It used to be the
// fragment's own guard alone, and the natural way to write a private area — a
// guarded layout, its content opened at a URL of its own for a script to
// refresh — served that content to anyone who asked the second URL.
func fragmentGuard(ctx context.Context, r *http.Request, page *types.Page, f *types.Fragment) (*types.GuardDecision, error) {
	guards := types.PageGuards(page)
	if f != page.ContentFragment && f.Guard != nil {
		guards = append(guards, f.Guard)
	}
	for _, guard := range guards {
		decision, err := guard(ctx, r)
		if err != nil {
			return nil, fmt.Errorf("collage: fragment %q: %w", f.Name, err)
		}
		if decision == nil {
			continue
		}
		if err := decision.Validate(); err != nil {
			return nil, fmt.Errorf("collage: fragment %q: %w", f.Name, err)
		}
		return decision, nil
	}
	return nil, nil
}

// fragmentPath is what a fragment path's action renders: which fragment of which
// page. RenderFragment reads it to resolve a URL back to the fragment.
type fragmentPath struct {
	page     *types.Page
	fragment *types.Fragment
}

// ErrNilFragmentPath reports a fragment path declared with no fragment to render.
var ErrNilFragmentPath = errors.New("collage: fragment path has no fragment")
