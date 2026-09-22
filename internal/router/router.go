// Package router resolves incoming HTTP requests to pages, redirects, or a
// not-found result: a radix tree per locale for page paths, a shared radix tree
// for redirects, and locale resolution from the request path, Accept-Language
// header, and a cookie.
package router

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Elagoht/collage/internal/types"
)

// ErrRedirectShadowsPage is returned at registration time when a redirect's From
// pattern normalizes to the same path as a page already registered for some
// locale, in either registration order: the redirect would make the page
// unreachable, or the page would make the redirect unreachable, whichever was
// registered second.
var ErrRedirectShadowsPage = errors.New("collage: redirect shadows a registered page")

// ErrUnsubstitutedPlaceholder is returned at registration time when a redirect's
// To template contains a "{name}" placeholder that is not among the parameter
// names its From pattern captures, since such a placeholder could never be
// substituted at match time.
var ErrUnsubstitutedPlaceholder = errors.New("collage: redirect placeholder not captured by from pattern")

// ErrUnsafeRedirectTarget is returned by Match when a redirect's destination, after
// its placeholders have been substituted, is not a single-slash-prefixed relative
// path — a protocol-relative "//host" or "/\host", or a destination carrying a
// control character. Such a destination goes straight into the Location header, so
// serving it would be an open redirect or a header injection; Match refuses it and
// the caller turns it into a 500 rather than a redirect.
//
// Substituted values are percent-escaped (see substitute), so this fires on a To
// template that is itself unsafe rather than on a hostile request parameter. It is
// checked at match time all the same: it is the last point before the value reaches
// the wire, and a check there cannot be bypassed by a Redirect constructed without
// going through a builder.
var ErrUnsafeRedirectTarget = errors.New("collage: unsafe redirect target")

// MatchResult describes what a request resolved to: a page, a redirect, or no
// match, always alongside the resolved locale.
type MatchResult struct {
	// Page is the matched page. It is nil when RedirectTo is set or IsNotFound is
	// true.
	Page *types.Page
	// Locale is the locale resolved for the request, always one of the router's
	// supported locales.
	Locale string
	// PathParams holds the dynamic and catch-all segment values captured by the
	// matched page's pattern, keyed by placeholder name. It is nil when a
	// redirect matched or nothing matched.
	PathParams map[string]string
	// RedirectTo is the destination path when a redirect matched, with its
	// placeholders already substituted and each substituted value
	// percent-escaped. It is always a relative path beginning with exactly one
	// "/" — see ErrUnsafeRedirectTarget — so it is safe to write straight into a
	// Location header. It is empty when no redirect matched.
	RedirectTo string
	// RedirectStatus is the HTTP status code for RedirectTo, taken from the
	// matched Redirect's EffectiveStatus. It is zero when no redirect matched.
	RedirectStatus int
	// IsNotFound reports whether neither a redirect nor a page matched the
	// request. This is not an error: Page is nil and RedirectTo is empty, but
	// Locale is still the request's resolved locale.
	IsNotFound bool
}

// Router resolves incoming requests to pages, redirects, or a not-found result,
// and holds the site's registered not-found and error pages.
type Router interface {
	// Match resolves req's locale and path to a MatchResult. A redirect takes
	// priority over a page match at the same path. Match returns an error only
	// for conditions the caller must react to specially — currently only
	// ErrUnsafeRedirectTarget, a redirect whose destination must not be served;
	// malformed request input — such as a segment that fails percent-decoding —
	// and an unmatched route both resolve normally, the latter with IsNotFound
	// true.
	Match(req *http.Request) (*MatchResult, error)
	// Register adds page's paths, across every locale in page.Paths, and its
	// Redirects to the router. It returns ErrInvalidPattern for a malformed
	// pattern, ErrDuplicateRoute when a path or redirect source is already
	// registered, ErrRedirectShadowsPage when a redirect's From collides with a
	// registered page path, and ErrUnsubstitutedPlaceholder when a redirect's To
	// references a placeholder its From does not capture.
	Register(page *types.Page) error
	// RegisterNotFound sets the page served when a request resolves to no
	// content.
	RegisterNotFound(page *types.Page) error
	// RegisterError sets the page served when rendering fails.
	RegisterError(page *types.Page) error
	// NotFoundPage returns the page registered by RegisterNotFound, or nil if
	// none was registered.
	NotFoundPage() *types.Page
	// ErrorPage returns the page registered by RegisterError, or nil if none was
	// registered.
	ErrorPage() *types.Page
}

// router is the default Router implementation.
type router struct {
	localeOptions LocaleOptions

	// pageTrees holds one radix tree per locale that has at least one
	// registered page path.
	pageTrees map[string]*node
	// redirectTree is shared across locales: Redirect carries no locale of its
	// own, so a registered redirect applies after locale resolution regardless
	// of which locale was resolved.
	redirectTree *node

	// pagePathsByLocale and redirectFroms record normalized pattern strings
	// already registered, keyed by locale for pages (Redirect.From carries no
	// locale) so ErrRedirectShadowsPage and ErrDuplicateRoute can name the prior
	// registration regardless of which order things were registered in.
	pagePathsByLocale map[string]map[string]*types.Page
	redirectFroms     map[string]*types.Redirect

	notFoundPage *types.Page
	errorPage    *types.Page
}

// New returns a Router that resolves locales according to opts.
func New(opts LocaleOptions) Router {
	return &router{
		localeOptions:     opts,
		pageTrees:         make(map[string]*node),
		redirectTree:      &node{},
		pagePathsByLocale: make(map[string]map[string]*types.Page),
		redirectFroms:     make(map[string]*types.Redirect),
	}
}

// Match implements Router.
func (rt *router) Match(req *http.Request) (*MatchResult, error) {
	// EscapedPath, not Path, is the source of truth for matching: Path is
	// already fully decoded by net/url, including a %2F into a literal "/",
	// which would erase the segment boundary a %2F is meant to stay inside of.
	// EscapedPath preserves the original encoding so splitting happens first,
	// on encoded segments, and decoding happens second, one segment at a time.
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}

	locale, remaining := resolveLocale(req, path, rt.localeOptions)

	segments, ok := decodeSegments(splitPath(remaining))
	if !ok {
		// Malformed percent-encoding from the wire is a 404, not a 500.
		return &MatchResult{Locale: locale, IsNotFound: true}, nil
	}

	if redirectNode, params, ok := rt.redirectTree.match(segments); ok {
		destination := substitute(redirectNode.redirectTo, params, redirectNode.redirectCatchAll)
		if reason, unsafe := unsafeRedirectReason(destination); unsafe {
			return nil, fmt.Errorf("%w: redirect to %q resolved to %q: %s", ErrUnsafeRedirectTarget, redirectNode.redirectTo, destination, reason)
		}
		return &MatchResult{
			Locale:         locale,
			RedirectTo:     destination,
			RedirectStatus: redirectNode.redirectStatus,
		}, nil
	}

	if tree, ok := rt.pageTrees[locale]; ok {
		if pageNode, params, ok := tree.match(segments); ok {
			return &MatchResult{
				Page:       pageNode.page,
				Locale:     locale,
				PathParams: params,
			}, nil
		}
	}

	return &MatchResult{Locale: locale, IsNotFound: true}, nil
}

// Register implements Router.
func (rt *router) Register(page *types.Page) error {
	for _, locale := range page.Locales() {
		pattern := page.Paths[locale]

		segments, err := parsePattern(pattern)
		if err != nil {
			return fmt.Errorf("collage: page %q: %w", page.Name, err)
		}

		normalized := normalizePattern(pattern)
		if existing, ok := rt.redirectFroms[normalized]; ok {
			return fmt.Errorf("%w: page %q path %q locale %q: redirect %q -> %q already registered there", ErrRedirectShadowsPage, page.Name, pattern, locale, existing.From, existing.To)
		}

		tree, ok := rt.pageTrees[locale]
		if !ok {
			tree = &node{}
			rt.pageTrees[locale] = tree
		}
		target, err := tree.insert(segments)
		if err != nil {
			return fmt.Errorf("collage: page %q path %q locale %q: %w", page.Name, pattern, locale, err)
		}
		if target.page != nil {
			return fmt.Errorf("%w: pattern %q locale %q: page %q already registered, page %q attempted", ErrDuplicateRoute, pattern, locale, target.page.Name, page.Name)
		}
		target.page = page

		byPath, ok := rt.pagePathsByLocale[locale]
		if !ok {
			byPath = make(map[string]*types.Page)
			rt.pagePathsByLocale[locale] = byPath
		}
		byPath[normalized] = page
	}

	for _, redirect := range page.Redirects {
		if err := rt.registerRedirect(redirect); err != nil {
			return fmt.Errorf("collage: page %q: %w", page.Name, err)
		}
	}

	return nil
}

// registerRedirect validates and inserts redirect into rt.redirectTree.
func (rt *router) registerRedirect(redirect *types.Redirect) error {
	fromSegments, err := parsePattern(redirect.From)
	if err != nil {
		return fmt.Errorf("redirect from %q: %w", redirect.From, err)
	}

	normalizedFrom := normalizePattern(redirect.From)
	for _, byPath := range rt.pagePathsByLocale {
		if page, ok := byPath[normalizedFrom]; ok {
			return fmt.Errorf("%w: redirect from %q: page %q is registered at that path", ErrRedirectShadowsPage, redirect.From, page.Name)
		}
	}

	captured := make(map[string]struct{}, len(fromSegments))
	catchAll := ""
	for _, seg := range fromSegments {
		if seg.kind == segmentStatic {
			continue
		}
		captured[seg.text] = struct{}{}
		if seg.kind == segmentCatchAll {
			// parsePattern guarantees at most one, as the final segment.
			catchAll = seg.text
		}
	}
	for _, name := range placeholderNames(redirect.To) {
		if _, ok := captured[name]; !ok {
			return fmt.Errorf("%w: redirect %q -> %q: placeholder %q", ErrUnsubstitutedPlaceholder, redirect.From, redirect.To, name)
		}
	}

	target, err := rt.redirectTree.insert(fromSegments)
	if err != nil {
		return fmt.Errorf("redirect from %q: %w", redirect.From, err)
	}
	if target.hasRedirect {
		return fmt.Errorf("%w: redirect pattern %q already registered", ErrDuplicateRoute, redirect.From)
	}
	target.hasRedirect = true
	target.redirectTo = redirect.To
	target.redirectStatus = redirect.EffectiveStatus()
	target.redirectCatchAll = catchAll
	rt.redirectFroms[normalizedFrom] = redirect

	return nil
}

// RegisterNotFound implements Router.
func (rt *router) RegisterNotFound(page *types.Page) error {
	rt.notFoundPage = page
	return nil
}

// RegisterError implements Router.
func (rt *router) RegisterError(page *types.Page) error {
	rt.errorPage = page
	return nil
}

// NotFoundPage implements Router.
func (rt *router) NotFoundPage() *types.Page {
	return rt.notFoundPage
}

// ErrorPage implements Router.
func (rt *router) ErrorPage() *types.Page {
	return rt.errorPage
}

// decodeSegments percent-decodes each of raw's segments individually via
// url.PathUnescape, returning the decoded segments and true, or nil and false if
// any segment fails to decode. Decoding happens per segment, after splitting on
// "/" — never on the joined path — so a percent-encoded "/" ("%2F") inside one
// segment is decoded into a literal "/" within that segment's value rather than
// being mistaken for a path separator during matching.
func decodeSegments(raw []string) ([]string, bool) {
	decoded := make([]string, len(raw))
	for i, seg := range raw {
		d, err := url.PathUnescape(seg)
		if err != nil {
			return nil, false
		}
		decoded[i] = d
	}
	return decoded, true
}

// placeholderNames returns, in order, the names of the "{name}" placeholders
// appearing in s.
func placeholderNames(s string) []string {
	var names []string
	for i := 0; i < len(s); {
		if s[i] != '{' {
			i++
			continue
		}
		end := strings.IndexByte(s[i:], '}')
		if end == -1 {
			break
		}
		names = append(names, s[i+1:i+end])
		i += end + 1
	}
	return names
}

// substitute replaces every "{name}" placeholder in template with params[name],
// percent-escaping each substituted value before it is written.
//
// The escaping is what keeps a redirect destination structural: Match decodes each
// path segment individually, so a request for "/old/%2Fevil.com" against
// "/old/{slug}" captures slug == "/evil.com", and pasting that into "/{slug}" would
// produce the protocol-relative "//evil.com" — an open redirect. The same goes for a
// captured "?", "#", "\", or CR/LF, each of which would change which resource the
// Location header names or what headers follow it. url.PathEscape escapes every one
// of those and leaves an ordinary slug untouched, so a value can only ever land in
// the destination as one path segment's worth of text.
//
// catchAll, when non-empty, names the single parameter whose captured value is a
// whole path tail rather than one segment. Its "/" separators are structure the
// pattern asked for, so they are preserved and only the segments between them are
// escaped. Every other parameter is escaped whole, separators included.
func substitute(template string, params map[string]string, catchAll string) string {
	var b strings.Builder
	for i := 0; i < len(template); {
		if template[i] != '{' {
			b.WriteByte(template[i])
			i++
			continue
		}
		end := strings.IndexByte(template[i:], '}')
		if end == -1 {
			b.WriteString(template[i:])
			break
		}
		name := template[i+1 : i+end]
		if catchAll != "" && name == catchAll {
			b.WriteString(escapePathTail(params[name]))
		} else {
			b.WriteString(url.PathEscape(params[name]))
		}
		i += end + 1
	}
	return b.String()
}

// escapePathTail percent-escapes each "/"-separated piece of value and rejoins them
// with "/", so a catch-all's captured path tail keeps its segment boundaries while
// everything inside a segment is escaped.
func escapePathTail(value string) string {
	parts := strings.Split(value, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// unsafeRedirectReason reports whether destination is unsafe to write into a
// Location header, and why. A safe destination is a relative path: it begins with
// exactly one "/", the character after it is neither "/" nor "\", and it carries no
// control character.
//
// Requiring the single leading "/" is what rejects an absolute URL with a scheme
// ("https://evil.com" and "javascript:...") — neither can begin with "/" — as well
// as the schemeless "//evil.com" and the "/\evil.com" that browsers normalise to it.
// The control-character scan rejects CR and LF, which net/http happens to reject
// too; relying on that would leave the guarantee owned by another package's
// implementation detail rather than by this one.
func unsafeRedirectReason(destination string) (string, bool) {
	if destination == "" {
		return "destination is empty", true
	}
	if destination[0] != '/' {
		return `destination is not a relative path starting with "/"`, true
	}
	if len(destination) > 1 && (destination[1] == '/' || destination[1] == '\\') {
		return "destination is protocol-relative", true
	}
	for i := 0; i < len(destination); i++ {
		if c := destination[i]; c < 0x20 || c == 0x7f {
			return fmt.Sprintf("destination contains the control character %#02x at offset %d", c, i), true
		}
	}
	return "", false
}
