package types

import (
	"errors"
	"fmt"
)

// Routes is what RenderContext.URL and RenderContext.ActionURL build links
// with: the application's own URL builders, bound by collage to every render and
// action it runs. Page backs {{pageURL}}, Action {{actionURL}}; DefaultLocale is
// the locale both fall back to for a route with no path in the render's own.
type Routes struct {
	Page          func(name, locale string, params map[string]string) (string, error)
	Action        func(name, locale string, params map[string]string) (string, error)
	DefaultLocale string
}

// BindRoutes gives rc — and every copy of it the render makes — the builders
// behind URL and ActionURL. Collage calls it; an application has no reason to.
func BindRoutes(rc *RenderContext, routes Routes) {
	if rc != nil && rc.state != nil {
		rc.state.routes = &routes
	}
}

// URL returns the path of the page or document registered as name, in the
// render's locale — what {{pageURL}} renders in a template — for a data handler
// or an action that needs it in Go:
//
//	location, err := rc.URL("story", map[string]string{"id": "42"})
//
// It falls back to the default locale for a route with no path in rc.Locale, as
// {{pageURL}} does. It is as strict as App.URL, and builds through the same
// path builder: a value is one escaped path segment, and one holding "/" or
// being "." or ".." is ErrRouteParams, so a value from the request cannot turn
// the path into another site's URL. Outside a render collage runs — a
// RenderContext built by hand — it is ErrUnknownRoute.
func (rc *RenderContext) URL(name string, params map[string]string) (string, error) {
	routes, err := rc.routes(name)
	if err != nil {
		return "", err
	}
	return routes.build(routes.Page, name, rc.Locale, params)
}

// ActionURL returns the path of the action registered as name, in the render's
// locale — what {{actionURL}} renders in a template — for Go code that needs it.
// It falls back to the default locale as URL does, and is as strict as
// App.ActionURL.
func (rc *RenderContext) ActionURL(name string, params map[string]string) (string, error) {
	routes, err := rc.routes(name)
	if err != nil {
		return "", err
	}
	return routes.build(routes.Action, name, rc.Locale, params)
}

// routes returns the builders bound to rc, or ErrUnknownRoute when none are.
func (rc *RenderContext) routes(name string) (*Routes, error) {
	if rc == nil || rc.state == nil || rc.state.routes == nil {
		return nil, fmt.Errorf("%w: %q: no routes are known to this render", ErrUnknownRoute, name)
	}
	return rc.state.routes, nil
}

// build runs one builder in locale, then in the default locale when the route
// has no path in locale.
func (routes *Routes) build(
	builder func(name, locale string, params map[string]string) (string, error),
	name, locale string,
	params map[string]string,
) (string, error) {
	if builder == nil {
		return "", fmt.Errorf("%w: %q: no routes are known to this render", ErrUnknownRoute, name)
	}
	built, err := builder(name, locale, params)
	if errors.Is(err, ErrNoPathInLocale) && locale != routes.DefaultLocale {
		return builder(name, routes.DefaultLocale, params)
	}
	return built, err
}
