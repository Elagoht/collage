package collage

import (
	"errors"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

// PageBuilder builds a Page through a fluent chain of WithX calls. See the package
// doc comment for how builder errors are accumulated and why it is safe to ignore
// them until registration. A page built without WithContent records ErrMissingContent
// at Build time.
type PageBuilder struct {
	page *Page
	errs []error
}

// NewPage starts a PageBuilder for a page named name. The page's strategy defaults to
// StrategyDynamic until Static, Dynamic, or Incremental is called.
func NewPage(name string) *PageBuilder {
	return &PageBuilder{
		page: &Page{
			Name:  name,
			Paths: make(map[string]string),
		},
	}
}

// WithLayout sets the fragment that wraps the page's content fragment.
func (b *PageBuilder) WithLayout(f *Fragment) *PageBuilder {
	b.page.LayoutFragment = f
	return b
}

// WithContent sets the page's primary content fragment.
func (b *PageBuilder) WithContent(f *Fragment) *PageBuilder {
	b.page.ContentFragment = f
	return b
}

// WithPath registers the path pattern that reaches this page in locale.
func (b *PageBuilder) WithPath(locale, pattern string) *PageBuilder {
	b.page.Paths[locale] = pattern
	return b
}

// WithRedirect adds a redirect from the path pattern from to the path to, using
// status as the redirect's HTTP status code.
func (b *PageBuilder) WithRedirect(from, to string, status int) *PageBuilder {
	b.page.Redirects = append(b.page.Redirects, &Redirect{From: from, To: to, StatusCode: status})
	return b
}

// WithPermanentRedirect adds a permanent redirect from the path pattern from to the
// path to.
func (b *PageBuilder) WithPermanentRedirect(from, to string) *PageBuilder {
	b.page.Redirects = append(b.page.Redirects, &Redirect{From: from, To: to, Permanent: true})
	return b
}

// WithNotFoundPage sets the page served when a request under this page resolves to
// no content.
func (b *PageBuilder) WithNotFoundPage(p *Page) *PageBuilder {
	b.page.NotFoundPage = p
	return b
}

// WithErrorPage sets the page served when rendering this page fails.
func (b *PageBuilder) WithErrorPage(p *Page) *PageBuilder {
	b.page.ErrorPage = p
	return b
}

// Static sets the page's strategy to StrategyStatic: render once and serve from
// cache until explicitly invalidated.
func (b *PageBuilder) Static() *PageBuilder {
	b.page.Strategy = StrategyStatic
	return b
}

// Dynamic sets the page's strategy to StrategyDynamic: render on every request and
// never serve from cache. This is the default strategy for a page no strategy method
// is called on.
func (b *PageBuilder) Dynamic() *PageBuilder {
	b.page.Strategy = StrategyDynamic
	return b
}

// Incremental sets the page's strategy to StrategyIncremental and its CacheTTL to
// ttl: serve from cache until ttl elapses since the last render.
func (b *PageBuilder) Incremental(ttl time.Duration) *PageBuilder {
	b.page.Strategy = StrategyIncremental
	b.page.CacheTTL = ttl
	return b
}

// WithDependency appends tags to the page's cache dependency tags.
func (b *PageBuilder) WithDependency(tags ...string) *PageBuilder {
	b.page.DependencyTags = append(b.page.DependencyTags, tags...)
	return b
}

// WithSEO sets the SEO metadata value under key. value is opaque to the framework —
// it is stored as-is on Page.SEO and interpreted by whatever renders SEO metadata.
func (b *PageBuilder) WithSEO(key string, value any) *PageBuilder { // any: Page.SEO is opaque framework metadata (Task 1), mirrored here for the builder parameter
	if b.page.SEO == nil {
		b.page.SEO = make(map[string]any)
	}
	b.page.SEO[key] = value
	return b
}

// Build returns the Page constructed so far. It never panics and never returns nil
// for a non-nil builder, even if WithX calls recorded errors along the way, or if no
// content fragment was ever set (which records ErrMissingContent); call BuildErr to
// check whether any errors were recorded.
func (b *PageBuilder) Build() *Page {
	if b.page.ContentFragment == nil {
		b.errs = append(b.errs, types.ErrMissingContent)
	}
	return b.page
}

// BuildErr returns the errors accumulated by prior WithX calls and by Build, joined
// with errors.Join, or nil if none were recorded. Call it after Build.
func (b *PageBuilder) BuildErr() error {
	return errors.Join(b.errs...)
}
