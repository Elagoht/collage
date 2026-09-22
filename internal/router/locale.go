package router

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// LocaleOptions configures locale resolution: the default locale, the supported
// set, and which sources are consulted, in priority order (path, header, cookie,
// default). Each Disable* field's zero value (false) means that source is
// enabled — the same negative-flag polarity pkg/collage's LocaleConfig uses, so
// leaving a field unset can never silently disable a source.
type LocaleOptions struct {
	// Default is the locale used when no other source resolves a supported one.
	// It is always treated as supported, even if absent from Supported.
	Default string
	// Supported lists the locales, besides Default, a request may resolve to.
	Supported []string
	// DisablePathLocale disables resolving the locale from a leading path
	// segment, such as "/tr/blog/post".
	DisablePathLocale bool
	// DisableHeaderLocale disables resolving the locale from the Accept-Language
	// header.
	DisableHeaderLocale bool
	// CookieName is the cookie consulted for the locale. Empty means "locale".
	CookieName string
	// DisableCookieLocale disables resolving the locale from the cookie.
	DisableCookieLocale bool
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

// resolveLocale determines req's locale and the path to route-match after
// stripping any locale prefix, applying opts' sources in priority order: path
// prefix, Accept-Language header, cookie, then Default. An unsupported or
// malformed value at any stage falls through to the next rather than failing;
// resolveLocale always returns a supported locale.
func resolveLocale(req *http.Request, path string, opts LocaleOptions) (locale string, remaining string) {
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

	if !opts.DisableHeaderLocale {
		if resolved, ok := resolveAcceptLanguage(req.Header.Get("Accept-Language"), supported); ok {
			return resolved, path
		}
	}

	if !opts.DisableCookieLocale {
		cookieName := opts.CookieName
		if cookieName == "" {
			cookieName = "locale"
		}
		if cookie, err := req.Cookie(cookieName); err == nil {
			if value, err := url.PathUnescape(cookie.Value); err == nil {
				if resolved, ok := matchSupported(supported, value); ok {
					return resolved, path
				}
			}
		}
	}

	return opts.Default, path
}

// acceptEntry is one parsed Accept-Language entry: a language tag and its
// q-value.
type acceptEntry struct {
	tag string
	q   float64
}

// resolveAcceptLanguage parses header per RFC 7231's Accept-Language grammar and
// resolves the entry with the true highest q-value: entries are considered in
// descending q order, and for each one an exact tag match is tried before its
// primary-subtag match (so "tr-TR" matches a supported "tr") — the first entry,
// in q order, that matches either way wins. Exact-vs-subtag only breaks a tie
// within one entry; it never lets a weaker entry's exact match beat a stronger
// entry's subtag match, which is what "highest q wins" requires (RFC 4647
// lookup). A q outside [0, 1] is clamped into range. Malformed entries — an
// empty tag, an unparsable or missing q-value where "q=" is present, or an
// excluded q=0 — are skipped rather than causing a failure; header is
// attacker-controlled input and this never panics on it. A bare "*" is treated
// as an ordinary, never-matching tag: it has no primary subtag of its own to
// fall back on, so it simply never resolves and the caller falls through to the
// next locale source, which is the same outcome a dedicated "*" handler would
// produce here since resolveAcceptLanguage is never the last source consulted.
func resolveAcceptLanguage(header string, supported []string) (string, bool) {
	if header == "" {
		return "", false
	}

	var entries []acceptEntry
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		tag := part
		q := 1.0
		if idx := strings.IndexByte(part, ';'); idx >= 0 {
			tag = strings.TrimSpace(part[:idx])
			qValue, hasQ := "", false
			for _, param := range strings.Split(part[idx+1:], ";") {
				param = strings.TrimSpace(param)
				if strings.HasPrefix(param, "q=") {
					qValue = strings.TrimPrefix(param, "q=")
					hasQ = true
				}
			}
			if hasQ {
				parsed, err := strconv.ParseFloat(qValue, 64)
				if err != nil {
					continue
				}
				q = parsed
			}
		}

		if q > 1 {
			q = 1
		} else if q < 0 {
			q = 0
		}

		if tag == "" || q <= 0 {
			continue
		}
		entries = append(entries, acceptEntry{tag: tag, q: q})
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].q > entries[j].q })

	for _, e := range entries {
		if resolved, ok := matchSupported(supported, e.tag); ok {
			return resolved, true
		}
		primary := e.tag
		if idx := strings.IndexByte(e.tag, '-'); idx >= 0 {
			primary = e.tag[:idx]
		}
		if resolved, ok := matchSupported(supported, primary); ok {
			return resolved, true
		}
	}
	return "", false
}
