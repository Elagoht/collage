package types

import (
	"fmt"
	"sort"
	"time"
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
	// StatusCode is the HTTP status code to use. Zero selects a default based on
	// Permanent; see EffectiveStatus.
	StatusCode int
	// Permanent reports whether the redirect should be treated as permanent when
	// StatusCode is zero.
	Permanent bool
}

// EffectiveStatus returns r.StatusCode when it is one of 301, 302, 307, or 308;
// otherwise it returns 301 when r.Permanent, else 302.
func (r *Redirect) EffectiveStatus() int {
	switch r.StatusCode {
	case 301, 302, 307, 308:
		return r.StatusCode
	}
	if r.Permanent {
		return 301
	}
	return 302
}

// Validate reports whether r is well-formed: From and To are both non-empty and
// start with "/" (ErrInvalidPath), and StatusCode is 0 or one of 301, 302, 307, 308
// (ErrInvalidRedirectStatus).
func (r *Redirect) Validate() error {
	if r.From == "" || r.From[0] != '/' {
		return fmt.Errorf("%w: redirect from %q must start with \"/\"", ErrInvalidPath, r.From)
	}
	if r.To == "" || r.To[0] != '/' {
		return fmt.Errorf("%w: redirect to %q must start with \"/\"", ErrInvalidPath, r.To)
	}
	switch r.StatusCode {
	case 0, 301, 302, 307, 308:
	default:
		return fmt.Errorf("%w: %d", ErrInvalidRedirectStatus, r.StatusCode)
	}
	return nil
}

// Page is a render configuration: the fragments to compose, the paths that reach it,
// and how its output is cached.
type Page struct {
	// Name identifies the page, primarily for diagnostics and error messages.
	Name string
	// LayoutFragment, when set, wraps ContentFragment and is the fragment tree's
	// root. The content fragment is bound to the layout's DefaultContentSlot slot by
	// the registration path (Task 11); Page itself never mutates its fragments.
	LayoutFragment *Fragment
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
	// SEO holds opaque SEO metadata for this page.
	SEO map[string]any // any: SEO metadata is opaque to the framework
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
}

// Root returns p.LayoutFragment when it is set, otherwise p.ContentFragment. It is
// the entry point for rendering p's fragment tree.
func (p *Page) Root() *Fragment {
	if p.LayoutFragment != nil {
		return p.LayoutFragment
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

// ContentSlotName returns DefaultContentSlot, the slot name a page's content
// fragment is bound to when the page has a layout fragment.
func (p *Page) ContentSlotName() string {
	return DefaultContentSlot
}

// Validate reports whether p is well-formed: Name is non-empty (ErrEmptyName),
// ContentFragment is non-nil (ErrMissingContent), every entry in Paths starts with
// "/" (ErrInvalidPath), CacheTTL is not negative (ErrInvalidTTL), CacheTTL is
// positive when Strategy is StrategyIncremental (ErrMissingTTL), every redirect in
// Redirects validates, neither NotFoundPage nor ErrorPage is p itself
// (ErrSelfErrorPage), and p.Root() validates. NotFoundPage and ErrorPage are not
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
	if p.CacheTTL < 0 {
		return fmt.Errorf("%w: page %q", ErrInvalidTTL, p.Name)
	}
	if p.Strategy == StrategyIncremental && p.CacheTTL <= 0 {
		return fmt.Errorf("%w: page %q", ErrMissingTTL, p.Name)
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
	if err := p.Root().Validate(); err != nil {
		return err
	}
	return nil
}
