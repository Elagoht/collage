package collage

import (
	"errors"
	"time"

	"github.com/Elagoht/collage/internal/core"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/types"
)

// Document is a routed, cacheable response that is not HTML: a sitemap, a feed, a
// robots.txt, a JWKS. It reuses a page's routing, caching, ETag and dependency-tag
// machinery, but renders no templates and composes no fragments — its handler
// returns bytes directly.
type Document = types.Document

// DocumentResult is one document's executed output, as returned by
// App.RenderDocumentPath: the body, the content type to serve it with, the
// dependency tags the body was derived from, whether it failed because the content
// does not exist, and how long the handler took. A caller MUST check
// RenderDocumentPath's error before reading Body.
//
// It is Result's sibling for documents, and exists for the same reason Result
// does: RenderDocumentPath is public surface reached through the App alias, so a
// caller must be able to name what it returns — to declare a variable, a struct
// field, or a helper's parameter — using nothing but this package.
type DocumentResult = render.DocumentResult

// DocumentHandlerFunc produces a document's body. It returns the bytes to serve,
// the dependency tags the body was derived from, and an error. Returning an error
// that wraps ErrNotFound makes the response a 404 rather than a 500.
type DocumentHandlerFunc = types.DocumentHandlerFunc

// ErrNilDocument is returned by RegisterDocument when passed a nil document.
var ErrNilDocument = types.ErrNilDocument

// ErrEmptyContentType is returned when a document declares no content type. A
// document's content type is static and required: the framework writes it on every
// response and never guesses it.
var ErrEmptyContentType = types.ErrEmptyContentType

// ErrNoDocumentHandler is returned when a document declares neither a handler nor a
// body. Unlike a page, a document has no template to fall back on, so it must have
// one or the other.
var ErrNoDocumentHandler = types.ErrNoDocumentHandler

// ErrDuplicateDocument is returned when a document is registered under a name
// another document already holds.
var ErrDuplicateDocument = core.ErrDuplicateDocument

// ErrDocumentNotFound is returned by App.RenderDocumentPath when a path resolves to
// no document — including when it resolves to a page or a redirect instead.
var ErrDocumentNotFound = core.ErrDocumentNotFound

// DocumentBuilder builds a Document through a fluent chain of WithX calls. See the
// package doc comment for how builder errors are accumulated and why it is safe to
// ignore them until registration. A document built with neither WithHandler nor
// WithBody records ErrNoDocumentHandler at Build time.
type DocumentBuilder struct {
	document *Document
	errs     []error
}

// NewDocument starts building a document named name and served at contentType.
func NewDocument(name, contentType string) *DocumentBuilder {
	return &DocumentBuilder{
		document: &Document{
			Name:        name,
			ContentType: contentType,
			Paths:       make(map[string]string),
		},
	}
}

// WithPath registers the path pattern that reaches this document in locale.
func (b *DocumentBuilder) WithPath(locale, pattern string) *DocumentBuilder {
	b.document.Paths[locale] = pattern
	return b
}

// AtRoot serves the document at pattern outside every locale: with no prefix,
// whatever the locale configuration — LocaleConfig.PrefixDefault included — and
// rendered in the default locale. It is for the files that belong to the site
// rather than to a language, which clients look for at a fixed address:
// /robots.txt, /llms.txt, /.well-known/security.txt.
//
// A link built by name reaches it from a page in any locale.
func (b *DocumentBuilder) AtRoot(pattern string) *DocumentBuilder {
	b.document.Paths[types.RootLocale] = pattern
	return b
}

// WithHandler sets the function that produces the document's body. A document
// with a handler and no declared strategy is dynamic.
func (b *DocumentBuilder) WithHandler(handler DocumentHandlerFunc) *DocumentBuilder {
	b.document.Handler = handler
	return b
}

// WithBody serves body, fixed when the program starts, in place of a handler:
//
//	collage.NewDocument("robots", "text/plain; charset=utf-8").
//		AtRoot("/robots.txt").
//		WithBody([]byte("User-agent: *\nAllow: /\n")).
//		Build()
//
// A document with a fixed body and no declared strategy is static, so it is
// cached and exported without saying so. Setting both a body and a handler is
// ErrConflictingData at registration.
func (b *DocumentBuilder) WithBody(body []byte) *DocumentBuilder {
	b.document.Body = body
	return b
}

// WithRedirect adds a redirect from the path pattern from to the path to, using
// status as the redirect's HTTP status code.
func (b *DocumentBuilder) WithRedirect(from, to string, status int) *DocumentBuilder {
	b.document.Redirects = append(b.document.Redirects, &Redirect{From: from, To: to, StatusCode: status})
	return b
}

// WithPermanentRedirect adds a permanent redirect from the path pattern from to the
// path to.
func (b *DocumentBuilder) WithPermanentRedirect(from, to string) *DocumentBuilder {
	b.document.Redirects = append(b.document.Redirects, &Redirect{From: from, To: to, Permanent: true})
	return b
}

// Static sets the document's strategy to StrategyStatic: render once and serve from
// cache until explicitly invalidated.
func (b *DocumentBuilder) Static() *DocumentBuilder {
	b.document.Strategy = StrategyStatic
	return b
}

// Dynamic sets the document's strategy to StrategyDynamic: render on every request
// and never serve from cache. It is what a document with a handler and no strategy
// method called on it resolves to; see StrategyAuto.
func (b *DocumentBuilder) Dynamic() *DocumentBuilder {
	b.document.Strategy = StrategyDynamic
	return b
}

// Incremental sets the document's strategy to StrategyIncremental and its CacheTTL
// to ttl: serve from cache until ttl elapses since the last render.
func (b *DocumentBuilder) Incremental(ttl time.Duration) *DocumentBuilder {
	b.document.Strategy = StrategyIncremental
	b.document.CacheTTL = ttl
	return b
}

// WithStaticParams lists the path parameter values a static build writes this
// document for, one map per file, on the same terms as PageBuilder.WithStaticParams.
func (b *DocumentBuilder) WithStaticParams(list StaticParamsFunc) *DocumentBuilder {
	b.document.StaticParams = list
	return b
}

// WithCacheParams restricts which query parameters take part in this document's
// cache key, on the same terms as PageBuilder.WithCacheParams.
func (b *DocumentBuilder) WithCacheParams(names ...string) *DocumentBuilder {
	params := make([]string, 0, len(names))
	params = append(params, names...)
	b.document.CacheParams = params
	return b
}

// WithDependency appends tags to the document's cache dependency tags.
func (b *DocumentBuilder) WithDependency(tags ...string) *DocumentBuilder {
	b.document.DependencyTags = append(b.document.DependencyTags, tags...)
	return b
}

// Build returns the Document constructed so far. It never panics and never returns
// nil for a non-nil builder, even if WithX calls recorded errors along the way, or
// if neither a handler nor a body was ever set (which records ErrNoDocumentHandler);
// call BuildErr to check whether any errors were recorded.
func (b *DocumentBuilder) Build() *Document {
	if b.document.Handler == nil && len(b.document.Body) == 0 {
		b.errs = append(b.errs, ErrNoDocumentHandler)
	}
	types.RecordBuildErr(b.document, b.BuildErr())
	return b.document
}

// BuildErr returns the errors accumulated by prior WithX calls and by Build, joined
// with errors.Join, or nil if none were recorded. Call it after Build.
func (b *DocumentBuilder) BuildErr() error {
	return errors.Join(b.errs...)
}
