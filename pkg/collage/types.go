// Package collage is the public API of the collage framework: a Go framework for
// server-side component-based rendering built from composable fragments and pages.
//
// This is the only package users import. It re-exports internal/types' domain types
// as Go type aliases (for example, "type Page = types.Page"), so a value built
// through this package's fluent builders and a value an internal package consumes are
// literally the same type — no conversion step, no interface{} boundary between them.
//
// # Builder error handling
//
// FragmentBuilder and PageBuilder accumulate errors instead of returning (*T, error)
// from every WithX call, because they are meant to be used in a single fluent chain,
// as in:
//
//	f := collage.NewFragment("home", "pages/home.html").WithDataHandler(h).Build()
//
// A WithX method that can fail records the error on the builder and keeps returning
// the builder unchanged in every other respect; call BuildErr to retrieve whatever was
// accumulated. Ignoring BuildErr does not lose a builder mistake silently: registering
// a page calls Page.Validate and rejects a malformed page with a named error before it
// ever serves a request, so a builder mistake surfaces loudly at startup rather than on
// the first request. Prefer checking BuildErr when a builder's inputs are not known to
// be well-formed ahead of time.
package collage

import "github.com/Elagoht/collage/internal/types"

// Fragment is the framework's unit of composition: a template, an optional data
// contract, and the slots it exposes to child fragments.
type Fragment = types.Fragment

// SlotDefinition declares a named position inside a fragment's template and the
// fragments bound to it.
type SlotDefinition = types.SlotDefinition

// DataHandlerFunc fetches the data a fragment renders with.
type DataHandlerFunc = types.DataHandlerFunc

// Page is a render configuration: the fragments to compose, the paths that reach it,
// and how its output is cached.
type Page = types.Page

// Redirect describes a source path pattern that redirects to a destination.
type Redirect = types.Redirect

// RenderContext carries request-scoped state to every fragment in one render.
type RenderContext = types.RenderContext

// RenderStrategy selects how a page's output is cached and regenerated.
type RenderStrategy = types.RenderStrategy

// StrategyDynamic renders on every request and never serves from cache.
const StrategyDynamic = types.StrategyDynamic

// StrategyStatic renders once and serves from cache until explicitly invalidated.
const StrategyStatic = types.StrategyStatic

// StrategyIncremental serves from cache until the page's CacheTTL elapses.
const StrategyIncremental = types.StrategyIncremental

// ErrNilFragment is returned when a nil *Fragment is used where a non-nil fragment is
// required.
var ErrNilFragment = types.ErrNilFragment

// ErrUnknownSlot is returned when an operation references a slot name a fragment has
// not declared.
var ErrUnknownSlot = types.ErrUnknownSlot

// ErrSlotOccupied is returned when binding a fragment to a slot that already has a
// fill and does not allow multiple.
var ErrSlotOccupied = types.ErrSlotOccupied

// ErrFragmentCycle is returned when a fragment tree contains a fragment reachable
// from itself.
var ErrFragmentCycle = types.ErrFragmentCycle

// ErrMissingContent is returned when a page has no content fragment.
var ErrMissingContent = types.ErrMissingContent

// ErrMissingTTL is returned when a page uses StrategyIncremental without a positive
// CacheTTL.
var ErrMissingTTL = types.ErrMissingTTL

// ErrInvalidTimeout is returned when a fragment's Timeout is negative.
var ErrInvalidTimeout = types.ErrInvalidTimeout

// ErrInvalidTTL is returned when a page's CacheTTL is negative.
var ErrInvalidTTL = types.ErrInvalidTTL

// ErrRequiredSlotUnfilled is returned when a fragment declares a slot as Required
// but the slot has no fill.
var ErrRequiredSlotUnfilled = types.ErrRequiredSlotUnfilled

// ErrInvalidSlotDefinition is returned when a fragment's slot map contains a
// malformed entry.
var ErrInvalidSlotDefinition = types.ErrInvalidSlotDefinition

// ErrInvalidRedirectStatus is returned when a redirect's status code is set to a
// value other than 0, 301, 302, 307, or 308.
var ErrInvalidRedirectStatus = types.ErrInvalidRedirectStatus

// ErrSelfErrorPage is returned when a page references itself as its own NotFoundPage
// or ErrorPage.
var ErrSelfErrorPage = types.ErrSelfErrorPage

// ErrEmptyName is returned when a required Name field is empty.
var ErrEmptyName = types.ErrEmptyName

// ErrEmptyTemplatePath is returned when a fragment's TemplatePath is empty.
var ErrEmptyTemplatePath = types.ErrEmptyTemplatePath

// ErrInvalidPath is returned when a path pattern does not start with "/".
var ErrInvalidPath = types.ErrInvalidPath

// ErrNotFound reports that a fragment's data does not exist, as distinct from a
// failure to fetch it. It is the one sentinel here that application code returns
// rather than receives: a DataHandler that wraps it — fmt.Errorf("...: %w",
// collage.ErrNotFound) — makes a Required fragment's failure render the page's
// NotFoundPage with a 404 instead of its ErrorPage with a 500. Without wrapping
// it, every missing record is a 500 and a page-specific 404 page is unreachable.
var ErrNotFound = types.ErrNotFound
