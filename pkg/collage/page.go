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
// cache until explicitly invalidated. The page's cache entry is written with no
// practical expiry, so Config.Cache.DefaultTTL does not apply to it — only
// InvalidateTags, or a cache eviction at MaxEntries, causes a re-render. A page
// that should expire on a clock wants Incremental instead.
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

// WithCacheParams restricts which query parameters take part in this page's cache
// key. Naming none at all — WithCacheParams() with no arguments — drops the query
// from the key entirely, which is how a page states that it renders the same
// whatever the query says.
//
// Without it every query parameter discriminates, which is correct but expensive: a
// crawler walking "?utm_source=..." variants mints an entry per variant and evicts
// the real archive from a bounded cache without ever asking for a distinct page.
// Name the parameters the page's data handlers actually read.
//
// It also canonicalises what survives, so "?page=2&sort=new" and "?sort=new&page=2"
// stop being two entries for one representation. That is only safe once the page
// has said which parameters matter, which is why it is not the default.
//
// Naming a parameter a handler does not read is harmless. Failing to name one it
// does read is not: two representations then share an entry, and one visitor is
// served another's page.
func (b *PageBuilder) WithCacheParams(names ...string) *PageBuilder {
	// Non-nil even when empty: nil means "every parameter", and a page that
	// deliberately ignores its query has to be able to say so.
	params := make([]string, 0, len(names))
	params = append(params, names...)
	b.page.CacheParams = params
	return b
}

// WithDependency appends tags to the page's cache dependency tags.
func (b *PageBuilder) WithDependency(tags ...string) *PageBuilder {
	b.page.DependencyTags = append(b.page.DependencyTags, tags...)
	return b
}

// WithSEO sets the SEO metadata value under key. value is opaque to the framework —
// it is stored as-is on Page.SEO and interpreted by whatever renders SEO metadata.
func (b *PageBuilder) WithSEO(key string, value any) *PageBuilder { // any: Page.SEO is opaque framework metadata, mirrored here for the builder parameter
	if b.page.SEO == nil {
		b.page.SEO = make(map[string]any)
	}
	b.page.SEO[key] = value
	return b
}

// WithAction gives this page's own URL a method it would otherwise refuse.
//
// This is what an HTML form needs: a form's action is the page it sits on, so the
// POST arrives at the page's URL. The action inherits the page's paths, in every
// locale the page declares.
//
//	collage.NewPage("new-post").
//		WithContent(form).
//		WithPath("en", "/posts/new").
//		WithAction("POST", createPost)
func (b *PageBuilder) WithAction(method string, h ActionHandlerFunc) *PageBuilder {
	b.page.Actions = append(b.page.Actions, &Action{
		Methods: []string{method},
		Handler: h,
	})
	return b
}

// WithActionFor attaches an action built with NewAction to this page's URL, for what
// the WithAction shorthand does not offer: several methods, a body limit, or the
// CSRF opt-out. Its own paths are replaced by the page's.
func (b *PageBuilder) WithActionFor(action *Action) *PageBuilder {
	b.page.Actions = append(b.page.Actions, action)
	return b
}

// WithFragmentPath opens one of this page's fragments at its own URL, so it can be
// fetched on its own: the search results without the page around them, a panel a
// fetch() refreshes, the row a form just created.
//
// Nothing is reachable unless it is declared here. A framework that exposed every
// fragment automatically would put every internal part of every page on the public
// web, and turning that off again is not something anyone remembers to do.
//
//	collage.NewPage("search").
//		WithContent(searchContent).
//		WithPath("en", "/search").
//		WithFragmentPath("en", "/search/results", resultsFragment)
func (b *PageBuilder) WithFragmentPath(locale, pattern string, f *Fragment) *PageBuilder {
	if b.page.FragmentPaths == nil {
		b.page.FragmentPaths = make(map[string]map[string]*Fragment)
	}
	if b.page.FragmentPaths[locale] == nil {
		b.page.FragmentPaths[locale] = make(map[string]*Fragment)
	}
	b.page.FragmentPaths[locale][pattern] = f
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
