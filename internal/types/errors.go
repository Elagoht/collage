package types

import "errors"

// ErrNilFragment is returned when a nil *Fragment is used where a non-nil fragment is
// required — as a Bind child, a Validate receiver, or elsewhere a fragment is expected.
var ErrNilFragment = errors.New("collage: nil fragment")

// ErrUnknownSlot is returned when an operation references a slot name a fragment has
// not declared — a Bind target or a Slot lookup that misses.
var ErrUnknownSlot = errors.New("collage: unknown slot")

// ErrUnknownAsset is returned when a template asks for the URL of a file no mount
// can resolve. It is an error rather than the path unchanged: a page that renders
// while linking a stylesheet that 404s reports itself as fine, and the typo
// survives to production.
var ErrUnknownAsset = errors.New("collage: unknown asset")

// ErrOnceTypeMismatch reports that one Once key was asked for as two different
// types within a single render. The value is whatever the first caller fetched; a
// second caller expecting something else is a bug in the keys, not a cache miss.
var ErrOnceTypeMismatch = errors.New("collage: once key fetched as two different types")

// ErrSlotOccupied is returned when binding a fragment to a slot that already has a
// fill and does not allow multiple.
var ErrSlotOccupied = errors.New("collage: slot already occupied")

// ErrFragmentCycle is returned when a fragment tree contains a fragment reachable
// from itself.
var ErrFragmentCycle = errors.New("collage: fragment cycle detected")

// ErrMissingContent is returned when a page has no content fragment.
var ErrMissingContent = errors.New("collage: missing content")

// ErrMissingTTL is returned when a page uses StrategyIncremental without a positive
// CacheTTL.
var ErrMissingTTL = errors.New("collage: missing cache ttl for incremental strategy")

// ErrInvalidTimeout is returned when a fragment's Timeout is negative.
var ErrInvalidTimeout = errors.New("collage: invalid timeout")

// ErrInvalidTTL is returned when a page's CacheTTL is negative. This is distinct
// from ErrMissingTTL, which covers a zero CacheTTL under StrategyIncremental.
var ErrInvalidTTL = errors.New("collage: invalid cache ttl")

// ErrRequiredSlotUnfilled is returned when a fragment declares a slot as Required
// but the slot has no fill.
var ErrRequiredSlotUnfilled = errors.New("collage: required slot has no fill")

// ErrInvalidSlotDefinition is returned when a fragment's slot map contains a
// malformed entry: an empty slot name, or a map key that does not match the
// SlotDefinition's own Name field.
var ErrInvalidSlotDefinition = errors.New("collage: invalid slot definition")

// ErrInvalidRedirectStatus is returned when a redirect's status code is set to a
// value other than 0, 301, 302, 307, or 308.
var ErrInvalidRedirectStatus = errors.New("collage: invalid redirect status code")

// ErrSelfErrorPage is returned when a page references itself as its own NotFoundPage
// or ErrorPage.
var ErrSelfErrorPage = errors.New("collage: page cannot reference itself as an error page")

// ErrEmptyName is returned when a required Name field is empty.
var ErrEmptyName = errors.New("collage: empty name")

// ErrEmptyTemplatePath is returned when a fragment's TemplatePath is empty.
var ErrEmptyTemplatePath = errors.New("collage: empty template path")

// ErrInvalidPath is returned when a path pattern does not start with "/".
var ErrInvalidPath = errors.New("collage: invalid path")

// ErrNotFound reports that a fragment's data does not exist, as distinct from a
// failure to fetch it. A DataHandler returns an error wrapping ErrNotFound to make
// the page render as 404 rather than 500.
var ErrNotFound = errors.New("collage: not found")

// ErrNilDocument reports that a nil document was supplied where one was required.
var ErrNilDocument = errors.New("collage: nil document")

// ErrEmptyContentType reports that a document declared no content type. A
// document's content type is static and required: the framework writes it on every
// response and never guesses it.
var ErrEmptyContentType = errors.New("collage: empty content type")

// ErrNoDocumentHandler reports that a document declared no handler. Unlike a page,
// a document has no template to fall back on, so a handler is mandatory.
var ErrNoDocumentHandler = errors.New("collage: document has no handler")

// ErrEmptyDocumentBody reports that a document's handler returned successfully but
// produced no body. A page may legitimately render nothing — an optional root
// fragment with no fallback produces an empty page on purpose — but a document has
// no such thing: its handler's return value IS the entire response, so an empty
// success is indistinguishable from a handler that forgot to populate it, not a
// valid empty document. It lives here rather than in internal/httpx or
// internal/build, the two packages that currently check for it, because it
// describes a property of a document's handler contract — DocumentHandlerFunc's
// own doc comment states the same rule — not of HTTP serving or of static
// building; both of those packages already import internal/types and reach it
// without a new dependency edge between them.
var ErrEmptyDocumentBody = errors.New("collage: document handler produced an empty body")

// ErrUnknownRoute is returned when a URL is asked for by a name no page or
// document was registered under.
var ErrUnknownRoute = errors.New("collage: no page or document by that name")

// ErrNoPathInLocale is returned when a URL is asked for in a locale the page or
// document has no path in.
var ErrNoPathInLocale = errors.New("collage: no path in that locale")

// ErrRouteParams is returned when the parameters given for a URL do not fill
// its pattern exactly: one is missing or empty, or one names no placeholder.
var ErrRouteParams = errors.New("collage: route parameters do not match the pattern")
