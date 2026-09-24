package core

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/types"
)

// ErrLocaleUnreachable is returned by URL for a locale no URL can reach: one the
// application does not support, or any locale but the default when path locales
// are off.
var ErrLocaleUnreachable = fmt.Errorf("collage: no URL reaches that locale")

// URL returns the path of the page or document registered as name, in locale,
// with params filling its pattern — the URL a reader follows to reach it, locale
// prefix included:
//
//	app.URL("about", "tr", nil)                                    // "/tr/hakkinda"
//	app.URL("blog-post", "en", map[string]string{"slug": "hello"}) // "/blog/hello"
//
// An empty locale is the default one. It is strict: a name nothing was registered
// under is ErrUnknownRoute, a locale the route has no path in is
// ErrNoPathInLocale, and params that do not fill the pattern exactly are
// ErrRouteParams — a link that cannot be built is a failure, not a guess.
//
// Templates reach it through {{pageURL}}, {{pageURLIn}} and {{localeURL}}.
func (a *App) URL(name, locale string, params map[string]string) (string, error) {
	if locale == "" {
		locale = a.cfg.Locale.Default
	}
	if !a.localeReachable(locale) {
		return "", fmt.Errorf("%w: %q", ErrLocaleUnreachable, locale)
	}

	a.mu.RLock()
	page := a.pages[name]
	document := a.documents[name]
	a.mu.RUnlock()

	var pattern string
	var found bool
	switch {
	case page != nil && document != nil:
		// Two registries, one namespace for links. Guessing which one was meant
		// is a link that works until somebody renames the other.
		return "", fmt.Errorf("%w: %q names both a page and a document", types.ErrUnknownRoute, name)
	case page != nil:
		pattern, found = page.PathFor(locale)
	case document != nil:
		pattern, found = document.PathFor(locale)
		if !found {
			// The site's own files are in no locale, so reachable from every one.
			if pattern, found = document.RootPath(); found {
				path, err := router.BuildPath(pattern, params)
				if err != nil {
					return "", fmt.Errorf("collage: URL for %q: %w", name, err)
				}
				return path, nil
			}
		}
	default:
		return "", fmt.Errorf("%w: %q", types.ErrUnknownRoute, name)
	}
	if !found {
		return "", fmt.Errorf("%w: %q has no path in %q", types.ErrNoPathInLocale, name, locale)
	}

	path, err := router.BuildPath(pattern, params)
	if err != nil {
		return "", fmt.Errorf("collage: URL for %q: %w", name, err)
	}
	path = a.prefixLocale(path, locale)
	// A page's, not a document's: "/sitemap.xml/" is not a file anyone serves.
	if page != nil && a.cfg.TrailingSlash && !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return path, nil
}

// localeReachable reports whether some URL can carry locale.
func (a *App) localeReachable(locale string) bool {
	if locale == a.cfg.Locale.Default {
		return true
	}
	return !a.cfg.Locale.DisablePathLocale && slices.Contains(a.cfg.Locale.Supported, locale)
}

// prefixLocale puts path under locale's prefix, which the default locale does
// not have unless PrefixDefault gives it one. The root is "/tr" rather than
// "/tr/", the form the router strips to "/".
func (a *App) prefixLocale(path, locale string) string {
	if locale == a.cfg.Locale.Default && !a.PrefixDefault() {
		return path
	}
	if path == "/" {
		return "/" + locale
	}
	return "/" + locale + path
}
