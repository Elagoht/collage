// Package router resolves incoming HTTP requests to pages, documents,
// redirects, or a not-found result: a radix tree per locale shared by page and
// document paths, a shared radix tree for redirects, and locale resolution
// from the request path — the only source of a locale there is.
package router

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Elagoht/collage/internal/types"
)

// ErrRedirectShadowsPage is returned at registration time when a redirect's From
// pattern normalizes to the same path as a page or document already registered
// for some locale, in either registration order: the redirect would make the
// page or document unreachable, or the page or document would make the
// redirect unreachable, whichever was registered second. The name predates
// documents sharing the page tree; it covers both route kinds, and a caller
// handles either collision identically.
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

// MatchResult describes what a request resolved to: a page, a document, a
// redirect, or no match, always alongside the resolved locale.
type MatchResult struct {
	// Page is the matched page. It is nil when RedirectTo is set or IsNotFound is
	// true.
	Page *types.Page
	// Document is the matched document. It is nil when a page matched, when
	// RedirectTo is set, or when IsNotFound is true. Exactly one of Page,
	// Document and Action is non-nil on a successful match.
	Document *types.Document
	// Action is the matched action: the route that answers this request's method.
	// It is nil when a page or a document answered instead.
	Action *types.Action
	// MethodNotAllowed reports that the path exists but answers no such method.
	// It is distinct from IsNotFound, and the distinction is the response: a 405
	// naming what the path does accept, rather than a 404 saying the URL is not
	// a URL.
	MethodNotAllowed bool
	// Allowed lists every method the matched path answers, sorted. It is set
	// whenever a path matched — on a successful match as well as a refused one —
	// because an OPTIONS request is a successful match that asks for exactly
	// this list.
	Allowed []string
	// Locale is the locale resolved for the request, always one of the router's
	// supported locales.
	Locale string
	// PathParams holds the dynamic and catch-all segment values captured by the
	// matched page's or document's pattern, keyed by placeholder name. It is
	// nil when a redirect matched or nothing matched.
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
	// IsNotFound reports whether neither a redirect, a page, nor a document
	// matched the request. This is not an error: Page, Document, and RedirectTo
	// are all empty, but Locale is still the request's resolved locale.
	IsNotFound bool
}

// ClaimedPath is one URL path pattern the router answers to, as reported by
// Router.ClaimedPaths. It exists so a caller outside this package can ask what URL
// space is already spoken for without enumerating the registries that space was
// built from — the router is the component that owns the answer, and a caller that
// rebuilt it from its own registries would be reading a copy that drifts.
type ClaimedPath struct {
	// Pattern is a request path pattern the router would answer, spelled the way
	// a request arrives: locale-prefixed forms are listed separately from the
	// bare form, because both reach the same route and both are claimed. It is
	// the pattern as registered, so a dynamic segment still appears as
	// "{slug}".
	Pattern string
	// Owner describes what claims Pattern, ready to drop into an error message:
	// `page "about"`, `document "sitemap"`, or `redirect from "/old-blog/{slug}"`.
	Owner string
}

// Router resolves incoming requests to pages, documents, redirects, or a
// not-found result, and holds the site's registered not-found and error
// pages.
type Router interface {
	// Match resolves req's locale and path to a MatchResult. A redirect takes
	// priority over a page or document match at the same path. Match returns an error only
	// for conditions the caller must react to specially — currently only
	// ErrUnsafeRedirectTarget, a redirect whose destination must not be served;
	// malformed request input — such as a segment that fails percent-decoding —
	// and an unmatched route both resolve normally, the latter with IsNotFound
	// true.
	Match(req *http.Request) (*MatchResult, error)
	// Register adds page's paths, across every locale in page.Paths, and its
	// Redirects to the router. It returns ErrInvalidPattern for a malformed
	// pattern, ErrDuplicateRoute when a path or redirect source is already
	// registered — including by an already-registered document —
	// ErrRedirectShadowsPage when a redirect's From collides with a registered
	// page or document path, and ErrUnsubstitutedPlaceholder when a redirect's
	// To references a placeholder its From does not capture.
	Register(page *types.Page) error
	// RegisterDocument adds document's paths, across every locale in
	// document.Paths, and its Redirects to the router. It returns
	// ErrInvalidPattern for a malformed pattern, ErrDuplicateRoute when a path
	// or redirect source is already registered — including by an
	// already-registered page — ErrRedirectShadowsPage when a redirect's From
	// collides with a registered page or document path, and
	// ErrUnsubstitutedPlaceholder when a redirect's To references a
	// placeholder its From does not capture.
	RegisterDocument(doc *types.Document) error
	// RegisterAction adds action's paths, across every locale in action.Paths, for
	// every method in action.Methods. An action shares a path with a page or a
	// document freely — a page and the POST its own form submits are one URL —
	// but two actions claiming one method for one path is ErrDuplicateRoute.
	RegisterAction(action *types.Action) error
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
	// ClaimedPaths returns every URL path pattern registered with the router, in
	// registration order: page paths, document paths, and redirect sources
	// alike, each with a description of what claims it. It is what lets a caller
	// ask "is this URL space already spoken for?" — internal/core's mount
	// close-out check is the caller it exists for.
	//
	// Every registry is included deliberately. A check that reads only pages and
	// documents misses redirects, which is how a mount came to silently swallow
	// a registered Redirect.From with no startup error and a 404 at request
	// time.
	//
	// Each locale-prefixed form of a pattern is listed alongside the bare one
	// whenever path-locale resolution is enabled, because a request for
	// "/tr/about" reaches the tr tree's "/about": the URL space a route occupies
	// includes the prefix a visitor actually types, not only the pattern the
	// route was registered under.
	ClaimedPaths() []ClaimedPath
}

// router is the default Router implementation.
type router struct {
	localeOptions LocaleOptions

	// routeTrees holds one radix tree per locale that has at least one
	// registered page or document path — see occupantName for how a single
	// tree node distinguishes the two.
	routeTrees map[string]*node
	// redirectTree is shared across locales: Redirect carries no locale of its
	// own, so a registered redirect applies after locale resolution regardless
	// of which locale was resolved.
	redirectTree *node

	// routedPathsByLocale and redirectFroms record normalized pattern strings
	// already registered, keyed by locale for routes (Redirect.From carries no
	// locale) so ErrRedirectShadowsPage and ErrDuplicateRoute can name the prior
	// registration regardless of which order things were registered in.
	// routedPathsByLocale's value is a formatted owner description — `page
	// "about"` or `document "sitemap"` — rather than *types.Page, so one map and
	// one shadow check (see checkRedirectShadow and registerRedirect) cover a
	// page and a document alike without a second, drifting copy of either.
	routedPathsByLocale map[string]map[string]string
	redirectFroms       map[string]*types.Redirect

	// claims records every registration in order, as the pattern was spelled,
	// so ClaimedPaths can report the URL space this router occupies without
	// re-deriving it from the maps above — whose keys are normalized ("/blog/{}")
	// and whose iteration order is random, neither of which belongs in an error
	// message.
	claims []claim

	notFoundPage *types.Page
	errorPage    *types.Page
}

// claim is one recorded registration: the locale it was registered under (empty
// for a redirect, which carries no locale), the pattern as written, and the
// description of what registered it.
type claim struct {
	locale  string
	pattern string
	owner   string
}

// New returns a Router that resolves locales according to opts.
func New(opts LocaleOptions) Router {
	return &router{
		localeOptions:       opts,
		routeTrees:          make(map[string]*node),
		redirectTree:        &node{},
		routedPathsByLocale: make(map[string]map[string]string),
		redirectFroms:       make(map[string]*types.Redirect),
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

	if target, redirect := canonicalLocalePath(path, rt.localeOptions); redirect {
		if req.URL.RawQuery != "" {
			target += "?" + req.URL.RawQuery
		}
		// Permanent, because the other spelling is never right. 308 rather than
		// 301 for anything but a read, so a form posted to the wrong spelling is
		// posted again rather than turned into a GET that drops it.
		status := http.StatusMovedPermanently
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			status = http.StatusPermanentRedirect
		}
		return &MatchResult{Locale: rt.localeOptions.Default, RedirectTo: target, RedirectStatus: status}, nil
	}

	locale, remaining := resolveLocale(path, rt.localeOptions)

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

	if tree, ok := rt.routeTrees[locale]; ok {
		if matched, params, ok := tree.match(segments); ok {
			allowed := allowedMethods(matched)

			// Answered here rather than by every action: OPTIONS asks what a URL
			// accepts, and the router is what knows. A handler is free to claim
			// it explicitly, and resolve hands the request over when one does.
			if req.Method == http.MethodOptions && matched.actions[http.MethodOptions] == nil {
				return &MatchResult{Locale: locale, PathParams: params, Allowed: allowed}, nil
			}

			page, doc, action, ok := resolve(matched, req.Method)
			if !ok {
				return &MatchResult{
					Locale:           locale,
					PathParams:       params,
					MethodNotAllowed: true,
					Allowed:          allowed,
					// Named so the 405 is answered in the document's own
					// kind — plain text — rather than as an HTML page.
					Document: matched.document,
				}, nil
			}
			return &MatchResult{
				Page:       page,
				Document:   doc,
				Action:     action,
				Locale:     locale,
				PathParams: params,
				Allowed:    allowed,
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
		owner := fmt.Sprintf("page %q", page.Name)
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
			return fmt.Errorf("collage: page %q path %q locale %q: %w", page.Name, pattern, locale, err)
		}
		if occupant := occupantName(target); occupant != "" {
			return fmt.Errorf("%w: page %q and %s both claim %q for locale %q",
				ErrDuplicateRoute, page.Name, occupant, pattern, locale)
		}
		target.page = page

		rt.recordRoutedPath(locale, pattern, normalized, owner)
	}

	return rt.registerRedirects(page.Name, page.Redirects)
}

// registerRedirects adds owner's redirects to the redirect tree. owner is used
// only to name the route in errors.
func (rt *router) registerRedirects(owner string, redirects []*types.Redirect) error {
	for _, redirect := range redirects {
		if err := rt.registerRedirect(redirect); err != nil {
			return fmt.Errorf("collage: %q: %w", owner, err)
		}
	}

	return nil
}

// checkRedirectShadow returns ErrRedirectShadowsPage when a redirect already
// registered at pattern's normalized form would make owner's route at pattern
// unreachable in locale. This is the "redirect registered first" half of the
// shadow check; registerRedirect's own lookup against routedPathsByLocale is
// the other half, for a redirect registered after the route. owner names the
// registering route for the error, e.g. `page "about"` or `document
// "sitemap"`. Register and RegisterDocument both call this — a page and a
// document colliding with an existing redirect must be rejected identically.
func (rt *router) checkRedirectShadow(owner, pattern, locale, normalized string) error {
	if existing, ok := rt.redirectFroms[normalized]; ok {
		return fmt.Errorf("%w: %s path %q locale %q: redirect %q -> %q already registered there",
			ErrRedirectShadowsPage, owner, pattern, locale, existing.From, existing.To)
	}
	return nil
}

// recordRoutedPath records that owner claims pattern's normalized form for
// locale, once Register or RegisterDocument has confirmed the registration
// otherwise succeeds. registerRedirect consults this so a redirect registered
// afterward at the same path is caught too — the "route registered first"
// half of the shadow check that checkRedirectShadow does not cover.
// It also records the claim ClaimedPaths reports, keeping the pattern as written
// rather than its normalized form: normalization exists to make two spellings of
// one route compare equal, and an error message that says "/blog/{}" instead of
// "/blog/{slug}" makes the reader hunt for a route that does not exist.
func (rt *router) recordRoutedPath(locale, pattern, normalized, owner string) {
	byPath, ok := rt.routedPathsByLocale[locale]
	if !ok {
		byPath = make(map[string]string)
		rt.routedPathsByLocale[locale] = byPath
	}
	byPath[normalized] = owner
	rt.claims = append(rt.claims, claim{locale: locale, pattern: pattern, owner: owner})
}

// registerRedirect validates and inserts redirect into rt.redirectTree.
func (rt *router) registerRedirect(redirect *types.Redirect) error {
	fromSegments, err := parsePattern(redirect.From)
	if err != nil {
		return fmt.Errorf("redirect from %q: %w", redirect.From, err)
	}

	normalizedFrom := normalizePattern(redirect.From)
	for _, byPath := range rt.routedPathsByLocale {
		if owner, ok := byPath[normalizedFrom]; ok {
			return fmt.Errorf("%w: redirect from %q: %s is registered at that path", ErrRedirectShadowsPage, redirect.From, owner)
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
	// Locale is empty: the redirect tree is shared across locales, so this
	// pattern is claimed under every one of them. ClaimedPaths expands that.
	rt.claims = append(rt.claims, claim{
		pattern: redirect.From,
		owner:   fmt.Sprintf("redirect from %q", redirect.From),
	})

	return nil
}

// ClaimedPaths implements Router.
func (rt *router) ClaimedPaths() []ClaimedPath {
	pathLocales := rt.pathLocales()

	claimed := make([]ClaimedPath, 0, len(rt.claims)*(1+len(pathLocales)))
	for _, c := range rt.claims {
		claimed = append(claimed, ClaimedPath{Pattern: c.pattern, Owner: c.owner})

		if c.locale != "" {
			// A route: reachable at its own pattern, and at the one
			// locale-prefixed form that resolves to its own locale.
			if slices.Contains(pathLocales, c.locale) {
				for _, prefixed := range localePrefixed(c.locale, c.pattern) {
					claimed = append(claimed, ClaimedPath{Pattern: prefixed, Owner: c.owner})
				}
			}
			continue
		}

		// A redirect: matched after the locale prefix is stripped, so every
		// supported locale's prefix reaches it.
		for _, locale := range pathLocales {
			for _, prefixed := range localePrefixed(locale, c.pattern) {
				claimed = append(claimed, ClaimedPath{Pattern: prefixed, Owner: c.owner})
			}
		}
	}
	return claimed
}

// pathLocales returns the locales that appear as a URL path prefix, or nil when
// path-locale resolution is disabled and none of them do.
func (rt *router) pathLocales() []string {
	if rt.localeOptions.DisablePathLocale {
		return nil
	}
	return rt.localeOptions.supportedLocales()
}

// localePrefixed returns the URL a request carrying locale's path prefix uses to
// reach pattern. Root is the one case worth spelling out: resolveLocale maps
// "/tr" — with no trailing segment — onto the pattern "/", so the prefixed form of
// "/" is "/tr" and not "/tr/".
// localePrefixed returns every URL form under which pattern is reachable in locale.
//
// The root pattern has two of them. resolveLocale maps a single-segment path to
// "/", so a page whose path is "/" answers at both "/tr" and "/tr/". Reporting only
// the unslashed form let a mount at "/tr/" through the shadow check, because that
// check is a prefix comparison and "/tr" is not prefixed by "/tr/" — the mount then
// swallowed the locale's front page without anything noticing at startup.
func localePrefixed(locale, pattern string) []string {
	if pattern == "/" {
		return []string{"/" + locale, "/" + locale + "/"}
	}
	return []string{"/" + locale + pattern}
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
