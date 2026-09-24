package core

import (
	"context"
	"errors"
	"fmt"
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
	return nil
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
// Each becomes an action answering GET, which is what it is: a request for a part of
// a page, answered by rendering that part. Reusing the action route rather than
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
				Methods: []string{"GET"},
				Handler: func(_ context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
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
		}
	}
	return nil
}

// ErrNilFragmentPath reports a fragment path declared with no fragment to render.
var ErrNilFragmentPath = errors.New("collage: fragment path has no fragment")
