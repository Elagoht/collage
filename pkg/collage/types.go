// Package collage is the public API of the collage framework: a Go framework for
// server-side component-based rendering built from composable fragments and pages.
//
// This is the only package users import. Its domain types are Go type aliases of
// the framework's own, so a value built through this package's fluent builders and
// the value the framework consumes are literally the same type — no conversion
// step and no untyped boundary between them.
//
// # Builder error handling
//
// FragmentBuilder, PageBuilder, DocumentBuilder and ActionBuilder accumulate errors
// instead of returning (*T, error) from every WithX call, because they are meant to
// be used in a single fluent chain, as in:
//
//	f := collage.NewFragment("home", "pages/home.html").WithData(collage.Load(loadHome)).Build()
//
// A WithX method that can fail records the error on the builder and keeps returning
// the builder unchanged in every other respect; call BuildErr to retrieve whatever was
// accumulated. Ignoring BuildErr does not lose a builder mistake silently: Build keeps
// what was recorded on the value it returns, and RegisterPage refuses a page whose
// builder, or the builder of any fragment reachable from it or action attached to
// it, recorded an error — as RegisterDocument and RegisterAction do for a document
// and an action — before Page.Validate even runs. A builder mistake therefore
// surfaces loudly at startup rather than on the first request. Prefer checking
// BuildErr when a builder's inputs are not known to be well-formed ahead of time.
//
// Build returns a copy: a WithX call after Build changes the builder and what a
// later Build returns, never a value already built or registered.
package collage

import "github.com/Elagoht/collage/internal/types"

// DefaultContentSlot is the name of the slot a page's content fragment is bound to
// when the page has a layout fragment. A layout renders it with {{slot "content"}},
// and registration fills it; the layout need not declare it.
const DefaultContentSlot = types.DefaultContentSlot

// Fragment is the framework's unit of composition: a template, an optional data
// contract, and the slots it exposes to child fragments.
type Fragment = types.Fragment

// SlotDefinition declares a named position inside a fragment's template and the
// fragments bound to it.
type SlotDefinition = types.SlotDefinition

// SlotResolverFunc returns the fragments a slot holds for one render. See
// FragmentBuilder.WithSlotResolver.
type SlotResolverFunc = types.SlotResolverFunc

// SlotRule constrains a slot declared with FragmentBuilder.WithSlot. A slot with
// no rule is optional and holds any number of fragments.
type SlotRule = types.SlotRule

const (
	// SlotRequired fails the render when nothing is bound to the slot.
	SlotRequired = types.SlotRequired
	// SlotSingle lets the slot hold one fragment at most.
	SlotSingle = types.SlotSingle
)

// GuardFunc decides whether a request may reach what it guards. The route's
// parameters are on the request, r.PathValue("slug"). See
// FragmentBuilder.WithGuard.
type GuardFunc = types.GuardFunc

// GuardDecision is a guard's answer about one request: a redirect (3xx status
// with a location) or a bare refusal status.
type GuardDecision = types.GuardDecision

// Page is a render configuration: the fragments to compose, the paths that reach it,
// and how its output is cached.
type Page = types.Page

// Registrable is what App.Register takes: a *Page, a *Document or an *Action.
// Nothing else satisfies it.
type Registrable = types.Registrable

// Redirect describes a source path pattern that redirects to a destination.
type Redirect = types.Redirect

// RenderContext carries request-scoped state to every fragment in one render.
type RenderContext = types.RenderContext

// Key names a value one render's fragments share and fixes its type. See
// NewKey.
type Key[T any] = types.Key[T]

// NewKey returns the key named name for values of type T:
//
//	var confirmKey = collage.NewKey[confirmView]("confirm")
//
//	confirmKey.Set(rc, view)
//	view, ok := confirmKey.Get(rc)
//
// A key is its name and its type, so two keys of one name never read each
// other's values. An empty name panics. Always make a key with NewKey: using a
// zero Key, which has no name, panics too.
func NewKey[T any](name string) Key[T] { return types.NewKey[T](name) }

// RenderValues are a finished render's shared values, as an AfterRender hook
// receives them in AfterRenderEvent.Values. A Key reads them with In.
type RenderValues = types.Values

// RenderStrategy selects how a page's output is cached and regenerated.
type RenderStrategy = types.RenderStrategy

// StaticParamsFunc returns, for locale, the path parameter values a static build
// writes a route for: one map per file, keyed by placeholder name. See
// PageBuilder.WithStaticParams.
type StaticParamsFunc = types.StaticParamsFunc

// StrategyAuto is the strategy of a page or document that declared none, resolved
// at registration: dynamic when a data handler, slot resolver, or document handler
// produces what it serves, static when everything in it is fixed.
const StrategyAuto = types.StrategyAuto

// StrategyDynamic renders on every request and never serves from cache.
const StrategyDynamic = types.StrategyDynamic

// StrategyStatic renders once and serves from cache until explicitly invalidated.
const StrategyStatic = types.StrategyStatic

// StrategyIncremental serves from cache until the page's CacheTTL elapses.
const StrategyIncremental = types.StrategyIncremental

// TemplateTypeError is one expression in a fragment's template that does not fit
// the fragment's data type, reported by RegisterPage. A page's findings come back
// joined; read each with errors.As.
type TemplateTypeError = types.TemplateTypeError
