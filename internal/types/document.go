package types

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// DocumentHandlerFunc produces a document's body. It returns the bytes to serve,
// the dependency tags the body was derived from, and an error. Returning an error
// that wraps ErrNotFound makes the response a 404 rather than a 500.
//
// Returning a nil or empty body alongside a nil error is treated as a failure, not
// as a valid empty response: a document has no equivalent of a page's optional root
// fragment, whose empty render is a deliberate, documented outcome, so an empty
// success here is indistinguishable from a handler that forgot to populate its
// body. A handler that genuinely wants to serve an empty document — a zero-byte
// robots.txt, say — can return a single newline instead.
type DocumentHandlerFunc func(ctx context.Context, rc *RenderContext) (body []byte, tags []string, err error)

// Document is a routed, cacheable response that is not HTML: a sitemap, a feed, a
// robots.txt, a JWKS. It reuses a page's routing, caching, ETag and dependency-tag
// machinery, but renders no templates and composes no fragments — its handler
// returns bytes directly.
type Document struct {
	// Name identifies the document and appears in registration errors.
	Name string
	// Paths maps a locale to the URL pattern that reaches this document. The key
	// RootLocale, "", is the document outside every locale: at its pattern with no
	// prefix, whatever the locale configuration. See RootPath.
	Paths map[string]string
	// ContentType is written verbatim as the response's Content-Type. It is static
	// by design: the framework reads it from the matched document at serve time, so
	// it never has to be stored alongside the cached body.
	ContentType string
	// Handler produces the body. It is required.
	Handler DocumentHandlerFunc
	// Strategy selects how the response is cached.
	Strategy RenderStrategy
	// CacheTTL is the lifetime of a cached body under StrategyIncremental.
	CacheTTL time.Duration
	// CacheParams restricts which query parameters take part in this document's
	// cache key, on the same terms as Page.CacheParams: nil keeps every parameter,
	// an empty non-nil value drops the query from the key.
	CacheParams []string
	// DependencyTags are tags every response from this document carries, in
	// addition to whatever its handler returns.
	DependencyTags []string
	// Redirects are source patterns that redirect to this document.
	Redirects []*Redirect
	// buildErr is what the builder that made this value recorded; see
	// RecordBuildErr. Registration refuses a value that carries one.
	buildErr error
}

// RootLocale is the Paths key of a document that belongs to the site rather than
// to a language: robots.txt, llms.txt, a /.well-known file. It is served at its
// pattern with no locale prefix, even when the default locale has one, and
// rendered in the default locale.
const RootLocale = ""

// RootPath returns the pattern the document is reachable at outside every
// locale, if it has one.
func (d *Document) RootPath() (string, bool) {
	if d == nil {
		return "", false
	}
	pattern, ok := d.Paths[RootLocale]
	return pattern, ok
}

// Locales returns the locales this document declares a path for, sorted.
func (d *Document) Locales() []string {
	if d == nil {
		return nil
	}
	locales := make([]string, 0, len(d.Paths))
	for locale := range d.Paths {
		locales = append(locales, locale)
	}
	slices.Sort(locales)
	return locales
}

// PathFor returns the URL pattern this document is reachable at for locale.
func (d *Document) PathFor(locale string) (string, bool) {
	if d == nil {
		return "", false
	}
	pattern, ok := d.Paths[locale]
	return pattern, ok
}

// Validate reports whether the document is coherent enough to register. It returns
// ErrNilDocument, ErrEmptyName, ErrEmptyContentType, ErrNoDocumentHandler,
// ErrInvalidPath, ErrInvalidTTL, ErrMissingTTL, or a redirect's own error.
func (d *Document) Validate() error {
	if d == nil {
		return ErrNilDocument
	}
	if d.Name == "" {
		return fmt.Errorf("%w: document", ErrEmptyName)
	}
	if strings.TrimSpace(d.ContentType) == "" {
		return fmt.Errorf("%w: document %q", ErrEmptyContentType, d.Name)
	}
	if d.Handler == nil {
		return fmt.Errorf("%w: document %q", ErrNoDocumentHandler, d.Name)
	}
	for _, locale := range d.Locales() {
		if pattern := d.Paths[locale]; !strings.HasPrefix(pattern, "/") {
			return fmt.Errorf("%w: document %q locale %q pattern %q", ErrInvalidPath, d.Name, locale, pattern)
		}
	}
	if d.CacheTTL < 0 {
		return fmt.Errorf("%w: document %q", ErrInvalidTTL, d.Name)
	}
	if d.Strategy == StrategyIncremental && d.CacheTTL <= 0 {
		return fmt.Errorf("%w: document %q", ErrMissingTTL, d.Name)
	}
	for _, redirect := range d.Redirects {
		if err := redirect.Validate(); err != nil {
			return fmt.Errorf("collage: document %q: %w", d.Name, err)
		}
	}
	return nil
}
