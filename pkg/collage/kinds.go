package collage

import "github.com/Elagoht/collage/internal/types"

// Route kinds: what a request resolved to, as RouteInfo reports it in
// Route.Kind. They are untyped string constants, so a comparison against the
// literal keeps compiling; the set may grow.
const (
	// RouteKindPage is a request answered by a page, and a request that resolved
	// to no route at all and was answered with the not-found page.
	RouteKindPage = types.RouteKindPage
	// RouteKindDocument is a request answered by a document.
	RouteKindDocument = types.RouteKindDocument
	// RouteKindAction is a request answered by an action.
	RouteKindAction = types.RouteKindAction
	// RouteKindMount is a request answered by a mounted file system.
	RouteKindMount = types.RouteKindMount
	// RouteKindHandler is a request answered by an http.Handler given to Handle.
	RouteKindHandler = types.RouteKindHandler
)

// Built file kinds: what a static build wrote, as BuildFinishedEvent reports it
// in BuiltFile.Kind. The set may grow.
const (
	// BuiltPage is a rendered page, the root copy of the home page and the
	// not-found page included.
	BuiltPage = types.BuiltPage
	// BuiltDocument is a document's body.
	BuiltDocument = types.BuiltDocument
	// BuiltAsset is a file copied from a mounted file system.
	BuiltAsset = types.BuiltAsset
)

// Pipeline stages: where in serving a request a failure happened, as ErrorEvent
// reports it in Stage. The set may grow, so an error hook names the stages it
// handles rather than assuming it has seen them all.
const (
	// StageRoute is resolving the request to a route, a method the route does
	// not answer and an action's request refused before it runs included.
	StageRoute = types.StageRoute
	// StageGuard is a guard failing.
	StageGuard = types.StageGuard
	// StageBeforeAction is a BeforeActionHook refusing or failing.
	StageBeforeAction = types.StageBeforeAction
	// StageHandler is an http.Handler given to Handle answering with a server
	// error.
	StageHandler = types.StageHandler
	// StageBeforeRender is a BeforeRenderHook refusing or failing.
	StageBeforeRender = types.StageBeforeRender
	// StageRender is rendering a page or a document, or running an action.
	StageRender = types.StageRender
	// StageAfterRender is an AfterRenderHook failing.
	StageAfterRender = types.StageAfterRender
	// StageCacheWrite is writing a rendered response to the cache.
	StageCacheWrite = types.StageCacheWrite
	// StageErrorPage is the error page itself failing to render.
	StageErrorPage = types.StageErrorPage
	// StageNotFound is a request that matched no route, or a plugin answering
	// 404 or 410 through ServeStatus.
	StageNotFound = types.StageNotFound
	// StageAsset is a mounted file system answering with an error status.
	StageAsset = types.StageAsset
	// StagePlugin is a plugin's hook failing outside the stages above, or a
	// plugin answering another error status through ServeStatus.
	StagePlugin = types.StagePlugin
	// StagePanic is a panic recovered while serving the request.
	StagePanic = types.StagePanic
)
