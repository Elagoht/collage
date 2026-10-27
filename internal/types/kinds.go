package types

// The kinds and stages the engine reports, defined once: the HTTP handler, the
// router and the static build emit these and nothing else, and pkg/collage
// publishes each under the same name. A kind or stage added here needs its public
// constant beside the others in pkg/collage, which a test there checks.

// Route kinds, as RouteInfo reports them in Route.Kind.
const (
	RouteKindPage     = "page"
	RouteKindDocument = "document"
	RouteKindAction   = "action"
	RouteKindMount    = "mount"
	RouteKindHandler  = "handler"
	RouteKindRejected = "rejected"
)

// RouteNameNormalise is the Route.Name of a request refused by path
// normalisation, under RouteKindRejected.
const RouteNameNormalise = "normalise"

// Built file kinds, as a static build reports them in BuiltFile.Kind.
const (
	BuiltPage     = "page"
	BuiltDocument = "document"
	BuiltAsset    = "asset"
)

// Pipeline stages, as reported to plugins in ErrorEvent.Stage.
const (
	StageRoute        = "route"
	StageGuard        = "guard"
	StageBeforeAction = "before_action"
	StageHandler      = "handler"
	StageBeforeRender = "before_render"
	StageRender       = "render"
	StageAfterRender  = "after_render"
	StageCacheWrite   = "cache_write"
	StageErrorPage    = "error_page"
	StageNotFound     = "not_found"
	StageAsset        = "asset"
	StagePlugin       = "plugin"
	StagePanic        = "panic"
)
