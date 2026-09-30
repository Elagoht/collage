package collage

import (
	"net/http"

	"github.com/Elagoht/collage/internal/core"
	"github.com/Elagoht/collage/internal/httpx"
)

// Vary declares that the response to r depends on the request header named
// header, and that the value the application resolved from it is value. Call it
// from middleware registered with App.Use, and read the value back in a data
// handler with Varied:
//
//	app.Use(func(next http.Handler) http.Handler {
//		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
//			lang := pickLanguage(r.Header.Get("Accept-Language")) // "tr" or "en"
//			collage.Vary(r, "Accept-Language", lang)
//			next.ServeHTTP(w, r)
//		})
//	})
//
//	// in the fragment's data handler:
//	lang, _ := collage.Varied(rc, "Accept-Language")
//
// The value, not the raw header, enters the page cache's key, so two languages are
// two entries rather than one per spelling of a browser's preferences. The header
// name goes into the response's Vary header, so a CDN between the server and the
// reader keeps them apart too.
//
// Read the value with Varied, not from the request context: a value a middleware
// stores in the context with context.WithValue is stripped from the render of a
// cacheable page, because that one render is served to every reader and a
// per-reader value in it would be the first reader's, frozen for the rest. A
// Vary value is safe to read there precisely because it is in the cache key, so
// each value has its own cached page.
//
// Without it, a cached page whose content depends on a header serves the first
// reader's version to everyone. It returns ErrVaryTooLate once routing has begun —
// from a data handler, say, whether or not the page is cached — and ErrVaryOutsideRequest for a
// request collage is not serving.
func Vary(r *http.Request, header, value string) error {
	return httpx.Vary(r, header, value)
}

// Varied returns the value a middleware declared for header through Vary on this
// request, and whether one was declared. It is how a data handler reads a cache
// dimension: the value is part of the page's cache key, so reading it in a render
// shared between readers is safe, where reading a raw context value is not — see
// Vary. rc is the render's context; pass the RenderContext a data handler
// receives.
//
//	lang, ok := collage.Varied(rc, "Accept-Language")
//
// It reports ("", false) for a header no middleware varied on.
func Varied(rc *RenderContext, header string) (string, bool) {
	if rc == nil {
		return "", false
	}
	return httpx.Varied(rc.Request, header)
}

// SkipCache declares, from middleware, that r is answered with a fresh render that
// is neither read from the page cache nor written to it, and marked private and
// no-store. It is what a preview is made of:
//
//	app.Use(func(next http.Handler) http.Handler {
//		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
//			if validPreviewCookie(r) { // yours to decide
//				collage.SkipCache(r)
//				r = r.WithContext(withDrafts(r.Context()))
//			}
//			next.ServeHTTP(w, r)
//		})
//	})
//
// An editor then sees the draft rather than the published page the cache holds,
// and nobody else is ever served the draft. Who may preview — a signed cookie, a
// session, a token from the CMS — is the application's own decision. It returns
// ErrVaryTooLate once routing has begun.
func SkipCache(r *http.Request) error {
	return httpx.SkipCache(r)
}

// ErrVaryTooLate is returned by Vary once the request's cache key has been
// computed. Declare dimensions from middleware.
var ErrVaryTooLate = httpx.ErrVaryTooLate

// ErrVaryOutsideRequest is returned by Vary for a request no collage handler is
// serving.
var ErrVaryOutsideRequest = httpx.ErrVaryOutsideRequest

// ErrInvalidHandlerPrefix is returned by App.Handle for a prefix that does not
// begin and end with "/", or is "/" alone.
var ErrInvalidHandlerPrefix = core.ErrInvalidHandlerPrefix

// ErrNilHandler is returned by App.Handle and App.Use when given nothing to run.
var ErrNilHandler = core.ErrNilHandler

// ErrHandlerFailed is reported to error hooks when a handler mounted with
// App.Handle answers with a server error.
var ErrHandlerFailed = httpx.ErrHandlerFailed
