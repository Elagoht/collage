package router

import (
	"net/url"
	"strings"
)

// LocaleOptions configures locale resolution: the default locale, the supported
// set, and whether a leading path segment selects one.
//
// The URL is the only source. A request is never assigned a locale from its
// Accept-Language header or a cookie: a page that answered /about in Turkish for
// one reader and in English for the next would be one URL with two contents,
// which is what caches, crawlers and shared links all get wrong. An application
// that wants to negotiate does so in its own middleware, and tells the cache
// with collage.Vary.
type LocaleOptions struct {
	// Default is the locale of a path with no supported locale prefix. It is
	// always treated as supported, even if absent from Supported.
	Default string
	// Supported lists the locales, besides Default, a request may resolve to.
	Supported []string
	// DisablePathLocale disables resolving the locale from a leading path
	// segment, such as "/tr/blog/post".
	DisablePathLocale bool
}

// supportedLocales returns opts.Supported with opts.Default appended if it is not
// already present, so Default is always a valid resolution target.
func (opts LocaleOptions) supportedLocales() []string {
	for _, s := range opts.Supported {
		if s == opts.Default {
			return opts.Supported
		}
	}
	return append(append([]string{}, opts.Supported...), opts.Default)
}

// matchSupported returns supported's own spelling of tag under a
// case-insensitive comparison, and whether one was found.
func matchSupported(supported []string, tag string) (string, bool) {
	for _, s := range supported {
		if strings.EqualFold(s, tag) {
			return s, true
		}
	}
	return "", false
}

// canonicalLocalePath returns where a request should be sent when its locale prefix
// is not the one URL that locale has: the default locale's own prefix, which the
// default locale's URLs do not carry, or a supported locale spelled in another case.
// Served as they are, /en/about and /TR/hakkinda would each be a second URL for a
// page — a duplicate for a search engine and a second entry in the cache.
func canonicalLocalePath(path string, opts LocaleOptions) (string, bool) {
	if opts.DisablePathLocale {
		return "", false
	}
	segments := splitPath(path)
	if len(segments) == 0 {
		return "", false
	}
	candidate, err := url.PathUnescape(segments[0])
	if err != nil {
		return "", false
	}
	resolved, ok := matchSupported(opts.supportedLocales(), candidate)
	if !ok || (resolved != opts.Default && candidate == resolved) {
		return "", false
	}
	rest := "/" + strings.Join(segments[1:], "/")
	if resolved == opts.Default {
		return rest, true
	}
	if rest == "/" {
		return "/" + resolved, true
	}
	return "/" + resolved + rest, true
}

// resolveLocale determines req's locale and the path to route-match after
// stripping any locale prefix: the leading path segment when it names a supported
// locale, and Default otherwise. It always returns a supported locale.
func resolveLocale(path string, opts LocaleOptions) (locale string, remaining string) {
	supported := opts.supportedLocales()
	segments := splitPath(path)

	if !opts.DisablePathLocale && len(segments) > 0 {
		if candidate, err := url.PathUnescape(segments[0]); err == nil {
			if resolved, ok := matchSupported(supported, candidate); ok {
				if len(segments) == 1 {
					return resolved, "/"
				}
				return resolved, "/" + strings.Join(segments[1:], "/")
			}
		}
	}
	return opts.Default, path
}
