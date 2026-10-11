package types

import (
	"fmt"
	"sort"
	"time"
	"unicode"
)

// DefaultContentSlot is the name of the slot a page's content fragment is bound to
// when the page has a layout fragment. See Page.ContentSlotName.
const DefaultContentSlot = "content"

// Redirect describes a source path pattern that redirects to a destination.
type Redirect struct {
	// From is the source path pattern that triggers the redirect.
	From string
	// To is the destination path the request is redirected to.
	To string
	// Status is the HTTP status code to use: 301, 302, 307 or 308. Zero means 302.
	Status int
}

// RedirectTextError reports a control character in a redirect's from or to,
// naming the field, wrapped in ErrInvalidRedirect; nil when both are clean.
func RedirectTextError(from, to string) error {
	for _, field := range []struct{ name, value string }{{"from", from}, {"to", to}} {
		for _, r := range field.value {
			if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
				return fmt.Errorf("%w: %s %q holds a control character", ErrInvalidRedirect, field.name, field.value)
			}
		}
	}
	return nil
}

// RedirectStatus returns r.Status when it is one of 301, 302, 307, or 308, and
// 302 otherwise.
func RedirectStatus(r *Redirect) int {
	switch r.Status {
	case 301, 302, 307, 308:
		return r.Status
	}
	return 302
}

// Validate reports whether r is well-formed: From and To are both non-empty and
// start with "/" (ErrInvalidPath), and Status is 0 or one of 301, 302, 307, 308
// (ErrInvalidRedirectStatus). A control character in either is ErrInvalidRedirect.
func (r *Redirect) Validate() error {
	if err := RedirectTextError(r.From, r.To); err != nil {
		return err
	}
	if r.From == "" || r.From[0] != '/' {
		return fmt.Errorf("%w: redirect from %q must start with \"/\"", ErrInvalidPath, r.From)
	}
	if r.To == "" || r.To[0] != '/' {
		return fmt.Errorf("%w: redirect to %q must start with \"/\"", ErrInvalidPath, r.To)
	}
	// A browser reads "//host" and "/\host" as another site. Caught here, at
	// registration, rather than as a 500 on the first request that matches.
	if len(r.To) > 1 && (r.To[1] == '/' || r.To[1] == '\\') {
		return fmt.Errorf("%w: redirect to %q leaves the site", ErrInvalidPath, r.To)
	}
	switch r.Status {
	case 0, 301, 302, 307, 308:
	default:
		return fmt.Errorf("%w: %d", ErrInvalidRedirectStatus, r.Status)
	}
	return nil
}

// Page is a render configuration: the fragments to compose, the paths that reach it,
// and how its output is cached.
type Page struct {
	// Name identifies the page, primarily for diagnostics and error messages.
	Name string
	// layoutFragment is the fragment tree's root once registration has folded
	// LayoutChain: the page's own copy of the outermost layout, with the rest of
	// the chain and the content fragment bound inside it. Before registration it
	// is the chain's outermost original, as WithLayouts sets it. It is unexported;
	// LayoutFragment and SetLayoutFragment read and write it. A page built by
	// hand with only a layout fragment set is taken as the one-layout chain; one
	// whose layout fragment is not LayoutChain[0] is refused. Page itself never
	// mutates its fragments.
	layoutFragment *Fragment
	// LayoutChain holds the page's layouts outermost-first, as the builder
	// declared them. Registration folds the chain into the layout fragment — a
	// per-page copy of each layout, the content fragment bound into the
	// innermost and each layout into the next outer one — and leaves this
	// field pointing at the originals, which is where a guard or an inspection
	// reads the spine from. Empty for a page with no layout.
	LayoutChain []*Fragment
	// ContentFragment is the page's primary content fragment. It is required.
	ContentFragment *Fragment
	// Paths maps locale to the path pattern that reaches this page in that locale.
	Paths map[string]string
	// Redirects lists the redirects registered alongside this page.
	Redirects []*Redirect
	// Strategy selects how this page's output is cached and regenerated.
	Strategy RenderStrategy
	// CacheTTL is how long a cached render stays valid under StrategyIncremental.
	CacheTTL time.Duration
	// StaticParams lists the path parameter values a static build writes this
	// page for, when a path pattern of it has a "{param}". It is consulted only by
	// a build; a running server answers every value the pattern matches.
	StaticParams StaticParamsFunc
	// CacheParams restricts which query parameters take part in this page's cache
	// key. A nil value keeps every parameter — the default, because narrowing by
	// default would silently merge two representations of a page whose handler
	// reads a parameter nobody remembered to declare. An empty non-nil value drops
	// the query from the key entirely, which is how a page states that it renders
	// the same whatever the query says.
	//
	// It exists because the raw query is otherwise a cache dimension in full: a
	// crawler walking "?utm_source=..." variants mints an entry per variant and
	// evicts the real archive from a bounded cache without ever requesting a
	// distinct page. Naming the parameters the page actually reads also lets the
	// key be canonicalised, so "?a=1&b=2" and "?b=2&a=1" stop being two entries for
	// one representation.
	CacheParams []string
	// DependencyTags lists the cache dependency tags this page's render depends on.
	DependencyTags []string
	// NotFoundPage is served when a request under this page resolves to no content.
	// It must not be the page itself.
	NotFoundPage *Page
	// ErrorPage is served when rendering this page fails. It must not be the page
	// itself.
	ErrorPage *Page
	// Actions are the non-GET methods this page's own URL answers, which is what
	// an HTML form needs: a form's action is the page it sits on. Each inherits
	// this page's Paths; an action that wants a URL of its own is registered on
	// its own instead.
	Actions []*Action
	// FragmentPaths are the fragments of this page that are reachable on their own,
	// keyed by locale and then by URL pattern. Declaring one is what opens it: a
	// framework that exposed every fragment by default would put every internal
	// part of every page on the public web, and turning that off again is not a
	// thing anyone remembers to do.
	FragmentPaths map[string]map[string]*Fragment
	// buildErr is what the builder that made this value recorded; see
	// RecordBuildErr. Registration refuses a value that carries one.
	buildErr error
}

// PageRoot returns p's layout fragment when it is set, otherwise p.ContentFragment. It is
// the entry point for rendering p's fragment tree.
func PageRoot(p *Page) *Fragment {
	if p.layoutFragment != nil {
		return p.layoutFragment
	}
	return p.ContentFragment
}

// Locales returns the sorted locale keys of p.Paths.
func (p *Page) Locales() []string {
	locales := make([]string, 0, len(p.Paths))
	for locale := range p.Paths {
		locales = append(locales, locale)
	}
	sort.Strings(locales)
	return locales
}

// PathFor returns the path pattern registered for locale and whether one was found.
func (p *Page) PathFor(locale string) (string, bool) {
	path, ok := p.Paths[locale]
	return path, ok
}

// PageContentSlotName returns DefaultContentSlot, the slot name a page's content
// fragment is bound to when the page has a layout fragment.
func PageContentSlotName(p *Page) string {
	return DefaultContentSlot
}

// PageGuarded reports whether any fragment on the page's spine carries a guard. It
// is the one question every path that serves a page outside a guarded request —
// response cache headers, a static build — asks before handing it out.
func PageGuarded(p *Page) bool {
	for _, f := range p.LayoutChain {
		if f != nil && f.Guard != nil {
			return true
		}
	}
	return p.ContentFragment != nil && p.ContentFragment.Guard != nil
}

// PageGuards returns the guards on the page's spine: each layout in LayoutChain,
// outermost first, then the content fragment. A nil guard — the common
// fragment — is skipped rather than represented. The first guard in this
// order is the first asked; see the httpx dispatch for what a decision does.
func PageGuards(p *Page) []GuardFunc {
	var out []GuardFunc
	for _, f := range p.LayoutChain {
		if f != nil && f.Guard != nil {
			out = append(out, f.Guard)
		}
	}
	if p.ContentFragment != nil && p.ContentFragment.Guard != nil {
		out = append(out, p.ContentFragment.Guard)
	}
	return out
}

// LayoutFragment returns p's layout fragment: nil for a page with no layout,
// the chain's outermost original before registration folds the chain, and the
// folded per-page copy after.
func LayoutFragment(p *Page) *Fragment { return p.layoutFragment }

// SetLayoutFragment sets p's layout fragment; see LayoutFragment.
func SetLayoutFragment(p *Page, f *Fragment) { p.layoutFragment = f }

// Validate reports whether p is well-formed: Name is non-empty (ErrEmptyName),
// ContentFragment is non-nil (ErrMissingContent), every entry in Paths starts with
// "/" (ErrInvalidPath), CacheTTL is not negative and is positive when Strategy is
// StrategyIncremental (ErrInvalidTTL), every redirect in
// Redirects validates, neither NotFoundPage nor ErrorPage is p itself
// (ErrSelfErrorPage), and its root fragment validates. NotFoundPage and ErrorPage are not
// recursed into: they are validated when registered in their own right.
func (p *Page) Validate() error {
	if p.Name == "" {
		return ErrEmptyName
	}
	if p.ContentFragment == nil {
		return ErrMissingContent
	}
	for locale, path := range p.Paths {
		if path == "" || path[0] != '/' {
			return fmt.Errorf("%w: path %q for locale %q must start with \"/\"", ErrInvalidPath, path, locale)
		}
	}
	if p.CacheTTL < 0 || (p.Strategy == StrategyIncremental && p.CacheTTL == 0) {
		return fmt.Errorf("%w: page %q", ErrInvalidTTL, p.Name)
	}
	for _, redirect := range p.Redirects {
		if err := redirect.Validate(); err != nil {
			return err
		}
	}
	if p.NotFoundPage != nil && p.NotFoundPage == p {
		return fmt.Errorf("%w: %q is its own NotFoundPage", ErrSelfErrorPage, p.Name)
	}
	if p.ErrorPage != nil && p.ErrorPage == p {
		return fmt.Errorf("%w: %q is its own ErrorPage", ErrSelfErrorPage, p.Name)
	}
	for _, fragment := range PagePathFragments(p) {
		if err := fragment.Validate(); err != nil {
			return err
		}
	}
	if err := PageRoot(p).Validate(); err != nil {
		return err
	}
	return nil
}

// PagePathFragments returns every fragment the page opened at a URL of its own with
// WithFragmentPath, each once, in a stable order.
func PagePathFragments(p *Page) []*Fragment {
	var out []*Fragment
	seen := make(map[*Fragment]bool)
	locales := make([]string, 0, len(p.FragmentPaths))
	for locale := range p.FragmentPaths {
		locales = append(locales, locale)
	}
	sort.Strings(locales)
	for _, locale := range locales {
		byPattern := p.FragmentPaths[locale]
		patterns := make([]string, 0, len(byPattern))
		for pattern := range byPattern {
			patterns = append(patterns, pattern)
		}
		sort.Strings(patterns)
		for _, pattern := range patterns {
			if fragment := byPattern[pattern]; fragment != nil && !seen[fragment] {
				seen[fragment] = true
				out = append(out, fragment)
			}
		}
	}
	return out
}
