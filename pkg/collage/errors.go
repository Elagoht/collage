package collage

import (
	"errors"

	"github.com/Elagoht/collage/internal/asset"
	"github.com/Elagoht/collage/internal/build"
	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/core"
	"github.com/Elagoht/collage/internal/csrf"
	datacache "github.com/Elagoht/collage/internal/datacache"
	"github.com/Elagoht/collage/internal/httpx"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/template"
	"github.com/Elagoht/collage/internal/types"
)

// Registering several things at once.
var (
	// ErrNilRegistrable is returned by Register for a nil item, where a page, a
	// document or an action was expected.
	ErrNilRegistrable = core.ErrNilRegistrable
)

// Registering an action.
var (
	// ErrNilAction is returned by RegisterAction for a nil action.
	ErrNilAction = core.ErrNilAction
	// ErrDuplicateAction is returned for a second action registered under a name
	// already taken.
	ErrDuplicateAction = core.ErrDuplicateAction
	// ErrEmptyActionName is returned for an action with no name.
	ErrEmptyActionName = core.ErrEmptyActionName
	// ErrNoActionPaths is returned for a standalone action with no path.
	ErrNoActionPaths = core.ErrNoActionPaths
	// ErrNoActionHandler is returned for an action with no handler.
	ErrNoActionHandler = core.ErrNoActionHandler
	// ErrNoActionMethods is returned for an action that answers no method.
	ErrNoActionMethods = router.ErrNoMethods
	// ErrInvalidActionMethod is returned for an action that declares OPTIONS,
	// TRACE or CONNECT. The router answers OPTIONS itself, and an action
	// claiming it would skip the forgery check.
	ErrInvalidActionMethod = router.ErrInvalidActionMethod
	// ErrStreamingBodyMethod is returned for an action that declares
	// WithStreamingBody but answers none of POST, PUT or PATCH.
	ErrStreamingBodyMethod = router.ErrStreamingBodyMethod
	// ErrNegativeBodyTimeout is returned for an action given a negative
	// WithBodyTimeout.
	ErrNegativeBodyTimeout = router.ErrNegativeBodyTimeout
	// ErrStreamingBody is returned by BeforeActionEvent.Form for an action
	// declared WithStreamingBody, without reading the body: such an action has
	// no form for a plugin to check.
	ErrStreamingBody = plugin.ErrStreamingBody
	// ErrNilFragmentPath is returned for a WithFragmentPath given no fragment.
	ErrNilFragmentPath = core.ErrNilFragmentPath
)

// Request-forgery refusals, reported to error hooks when a submission is refused
// with a 403.
var (
	// ErrCSRFMissing reports a submission that carried no token, or no cookie.
	ErrCSRFMissing = csrf.ErrMissing
	// ErrCSRFMismatch reports a token that is not the one in the cookie.
	ErrCSRFMismatch = csrf.ErrMismatch
	// ErrCSRFInvalid reports a token this application did not sign.
	ErrCSRFInvalid = csrf.ErrInvalid
	// ErrCSRFCrossOrigin reports a submission the browser marked as sent from
	// another origin — by Sec-Fetch-Site, or by an Origin that is not the Host —
	// refused whatever token it carried. An origin whose forms may post here is
	// named in Security.CSRFTrustedOrigins.
	ErrCSRFCrossOrigin = csrf.ErrCrossOrigin
	// ErrCSRFHeaderRequired reports a submission to a WithStreamingBody action
	// that did not carry its token in the header — the only place such an action
	// looks. It wraps ErrCSRFMissing.
	ErrCSRFHeaderRequired = csrf.ErrHeaderRequired
	// ErrCSRFDisabled is returned by {{csrfToken}} when forgery protection is off.
	ErrCSRFDisabled = core.ErrCSRFDisabled
)

// ErrMethodNotAllowed is reported for a request whose path exists but answers no
// such method: a 405, with an Allow header naming what it does answer.
var ErrMethodNotAllowed = httpx.ErrMethodNotAllowed

// ErrTemplateEscapesRoot is returned by New for a template path that resolves
// outside Template.Root.
var ErrTemplateEscapesRoot = template.ErrTemplateEscapesRoot

// ErrSourceConflict is returned at registration for an inline template that
// defines a template of its own, which would replace a file template of that name.
var ErrSourceConflict = template.ErrSourceConflict

// Configuration and startup.
var (
	// ErrInvalidPort is returned when Config.Server.Port is outside the valid TCP port
	// range 1..65535.
	ErrInvalidPort = errors.New("collage: invalid port")
	// ErrEmptyCacheDir is returned when Cache.Type is "disk" and Cache.Dir is empty.
	ErrEmptyCacheDir = cache.ErrEmptyCacheDir
	// ErrInvalidCacheType is returned when the cache is enabled, no Store is
	// supplied, and Type is neither "memory" nor "disk".
	ErrInvalidCacheType = core.ErrInvalidCacheType
	// ErrLocaleDefaultNotSupported is returned when Config.Locale.Default is not present
	// in Config.Locale.Supported.
	ErrLocaleDefaultNotSupported = errors.New("collage: default locale not in supported locales")
	// ErrNegativeDuration is returned when a Config duration field is negative. It is
	// shared deliberately across all eight duration fields Validate checks
	// (Server.ReadTimeout, Server.ReadHeaderTimeout, Server.WriteTimeout,
	// Server.IdleTimeout, Server.ShutdownTimeout, Server.DrainDelay, Template.Timeout,
	// Cache.DefaultTTL): unlike
	// ErrInvalidTimeout and ErrInvalidTTL, which mean genuinely different
	// things to a caller (a fragment's data-fetch deadline versus a page's cache
	// lifetime), a negative value in any of these eight fields is the same failure mode —
	// "you passed a negative duration" — differing only in which field tripped. Validate
	// names the offending field in the wrapped message (%w: %s); callers that need to
	// distinguish which field failed should inspect that message rather than expect a
	// per-field sentinel.
	ErrNegativeDuration = errors.New("collage: negative duration")
	// ErrInvalidOrigin is returned for an origin that is not scheme://host[:port]
	// with no path, query or fragment.
	ErrInvalidOrigin = plugin.ErrInvalidOrigin
	// ErrTemplateRootMissing is returned, wrapped, by New when Config.Template.Root
	// does not exist or is not a directory. It is the most common startup failure there
	// is, and it is worth telling apart from a template that exists but does not parse.
	ErrTemplateRootMissing = template.ErrTemplateRootMissing
	// ErrNilApp is returned by Build and DispatchCommands when passed a nil *App, or
	// one New did not return. There is nothing to build or dispatch against, and
	// inventing an application would run a program the caller never built.
	ErrNilApp = errors.New("collage: nil app")
	// ErrAppStarted is returned by the registration methods once the application has
	// started.
	ErrAppStarted = core.ErrAppStarted
	// ErrConfigurerRegisteredLate is returned by RegisterPlugin for a plugin
	// implementing Configurer, which must be supplied in Config.Plugins instead.
	ErrConfigurerRegisteredLate = core.ErrConfigurerRegisteredLate
)

// Registration: pages, fragments, commands, mounts and handlers.
var (
	// ErrDuplicatePage is returned when a page is registered under a name another page
	// already holds.
	ErrDuplicatePage = core.ErrDuplicatePage
	// ErrDuplicateCommand is returned by App.RegisterCommand for a name another command
	// already holds.
	ErrDuplicateCommand = core.ErrDuplicateCommand
	// ErrUnknownCommand is returned by DispatchCommands when args is empty or names no
	// command any registered plugin contributed. It is the signal to fall through to
	// whatever the program does by default — serving, building, or printing its own
	// usage — so test for it with errors.Is rather than treating every non-nil error as
	// fatal.
	ErrUnknownCommand = errors.New("collage: unknown command")
	// ErrNilFragment is returned when a nil *Fragment is used where a non-nil fragment is
	// required.
	ErrNilFragment = types.ErrNilFragment
	// ErrNilHandler is returned by App.Handle and App.Use when given nothing to run.
	ErrNilHandler = core.ErrNilHandler
	// ErrInvalidPrefix reports a mount prefix that is empty, "/", or not a
	// slash-delimited path. A mount at "/" would swallow every route.
	ErrInvalidPrefix = asset.ErrInvalidPrefix
	// ErrNilFS reports that a mount was given no file system.
	ErrNilFS = asset.ErrNilFS
	// ErrMountShadowsRoute reports that a mount prefix would swallow a registered page
	// or document path, making that route unreachable.
	ErrMountShadowsRoute = core.ErrMountShadowsRoute
	// ErrMountConflict reports that two mounts claim overlapping prefixes.
	ErrMountConflict = core.ErrMountConflict
	// ErrEmptyName is returned when a required Name field is empty.
	ErrEmptyName = types.ErrEmptyName
	// ErrEmptyTemplatePath is returned when a fragment's TemplatePath is empty.
	ErrEmptyTemplatePath = types.ErrEmptyTemplatePath
	// ErrConflictingTemplate is returned when a fragment names both a template file
	// and an inline template.
	ErrConflictingTemplate = types.ErrConflictingTemplate
	// ErrConflictingLayout is recorded when WithLayouts is called a second time on
	// one builder.
	ErrConflictingLayout = types.ErrConflictingLayout
	// ErrMissingLayout is recorded when WithLayouts is called with no layouts.
	ErrMissingLayout = types.ErrMissingLayout
	// ErrMissingContent is returned when a page has no content fragment.
	ErrMissingContent = types.ErrMissingContent
	// ErrConflictingData is returned when a fragment's data is set twice (WithData
	// called twice with data), or a document sets both WithBody and WithHandler.
	ErrConflictingData = types.ErrConflictingData
	// ErrInvalidTimeout is returned when a fragment's Timeout is negative.
	ErrInvalidTimeout = types.ErrInvalidTimeout
	// ErrInvalidTTL is returned when a page's or document's CacheTTL is negative, or
	// zero or less under StrategyIncremental: Incremental(0) records it.
	ErrInvalidTTL = types.ErrInvalidTTL
	// ErrSelfErrorPage is returned when a page references itself as its own NotFoundPage
	// or ErrorPage.
	ErrSelfErrorPage = types.ErrSelfErrorPage
	// ErrUnregisteredErrorPage is returned when the application starts and a registered
	// page references a NotFoundPage or ErrorPage that was never registered itself. An
	// unregistered error page never has its content bound into its layout, so it would
	// silently render empty at the moment it was needed.
	ErrUnregisteredErrorPage = core.ErrUnregisteredErrorPage
	// ErrUnregisteredPage is reported when an action answers with a page that was never
	// registered: register the page with RegisterPage, and answer with that same value.
	ErrUnregisteredPage = httpx.ErrUnregisteredPage
	// ErrInvalidGuardDecision is returned when a guard's decision cannot be
	// written as an answer.
	ErrInvalidGuardDecision = types.ErrInvalidGuardDecision
	// ErrDuplicateSlot is returned when FragmentBuilder.WithSlot is called twice with the
	// same slot name on the same fragment.
	ErrDuplicateSlot = errors.New("collage: slot already declared")
	// ErrInvalidSlotDefinition is returned when a fragment's slot map contains a
	// malformed entry.
	ErrInvalidSlotDefinition = types.ErrInvalidSlotDefinition
	// ErrUnknownSlot is returned at registration when a fragment binds into a slot its
	// template never calls — a fill that could never render.
	ErrUnknownSlot = types.ErrUnknownSlot
	// ErrSlotOccupied is returned when binding a fragment to a slot that already has a
	// fill and does not allow multiple.
	ErrSlotOccupied = types.ErrSlotOccupied
)

// Routing and URLs.
var (
	// ErrInvalidPattern is returned when a page path or redirect source is malformed:
	// it does not start with "/", contains an empty segment, contains a placeholder
	// with an empty name, holds two placeholders in one segment ("{name}.{ext}"),
	// places a catch-all ("{name...}") anywhere but last, or puts text around one.
	ErrInvalidPattern = router.ErrInvalidPattern
	// ErrDuplicateRoute is returned when a page path, or a redirect source, is already
	// registered for the same locale.
	ErrDuplicateRoute = router.ErrDuplicateRoute
	// ErrAmbiguousParameterName is returned when two patterns use different parameter
	// names at the same position, such as "/blog/{slug}" and "/blog/{id}/edit": a route
	// node carries one dynamic edge, so the two names cannot both be right.
	ErrAmbiguousParameterName = router.ErrAmbiguousParameterName
	// ErrOverlappingPattern is returned when two placeholders with text around them
	// at the same position cross: some segment matches both and neither is more
	// specific, such as "/a{x}" and "/{x}b", which "/aXb" matches both of. Disjoint
	// ones ("{slug}.md" and "{slug}.json") and nested ones ("{slug}.min.md" within
	// "{slug}.md", every one within "{slug}") are fine: the more specific wins,
	// whatever the order of registration.
	ErrOverlappingPattern = router.ErrOverlappingPattern
	// ErrRedirectShadowsPage is returned when a redirect's source path is also a
	// registered page path, in either registration order: one of the two would be
	// unreachable.
	ErrRedirectShadowsPage = router.ErrRedirectShadowsPage
	// ErrUnsubstitutedPlaceholder is returned when a redirect's destination contains a
	// "{name}" its source pattern does not capture, so it could never be substituted at
	// match time.
	ErrUnsubstitutedPlaceholder = router.ErrUnsubstitutedPlaceholder
	// ErrUnsafeRedirectTarget is the failure a plugin's ErrorHook receives, under the
	// stage "route", when a redirect's destination is not a single-slash-prefixed
	// relative path once its placeholders are substituted — a protocol-relative
	// "//host" or "/\host", or one carrying a control character. The framework
	// percent-escapes every substituted value, so this reports a redirect whose own To
	// is unsafe. The request is answered with a 500 rather than a Location header
	// nobody should follow.
	ErrUnsafeRedirectTarget = router.ErrUnsafeRedirectTarget
	// ErrInvalidRedirectStatus is returned when a redirect's status code is set to a
	// value other than 0, 301, 302, 307, or 308.
	ErrInvalidRedirectStatus = types.ErrInvalidRedirectStatus
	// ErrInvalidRedirect is returned for a redirect whose source or destination
	// holds a control character.
	ErrInvalidRedirect = types.ErrInvalidRedirect
	// ErrInvalidPath is returned when a path pattern does not start with "/".
	ErrInvalidPath = types.ErrInvalidPath
	// ErrUnknownRoute is returned by App.URL and the URL template functions for a
	// name no page or document was registered under.
	ErrUnknownRoute = types.ErrUnknownRoute
	// ErrNoPathInLocale is returned by App.URL and {{pageURLIn}} for a route with no
	// path in the locale asked for.
	ErrNoPathInLocale = types.ErrNoPathInLocale
	// ErrUnknownFragmentPath is returned by App.FragmentURL and {{fragmentURL}} for a
	// fragment the page did not open with WithFragmentPath.
	ErrUnknownFragmentPath = types.ErrUnknownFragmentPath
	// ErrAmbiguousFragmentPath is returned by App.FragmentURL and {{fragmentURL}} for
	// a fragment the page opened at more than one path in the locale asked for.
	ErrAmbiguousFragmentPath = types.ErrAmbiguousFragmentPath
	// ErrRouteParams is returned when the parameters given for a URL do not fill the
	// route's pattern exactly.
	ErrRouteParams = types.ErrRouteParams
	// ErrLocaleUnreachable is returned by App.URL for a locale no URL can carry, and
	// by RegisterPage, RegisterAction and RegisterDocument for a path in one — a
	// WithPath("tr", …) on a site whose Locale config leaves "tr" out, which would
	// otherwise answer every request with 404.
	ErrLocaleUnreachable = core.ErrLocaleUnreachable
	// ErrUnknownAsset is returned by rc.Asset, rc.HoistStylesheet, {{asset}} and
	// {{stylesheet}} for a path no mount serves.
	ErrUnknownAsset = types.ErrUnknownAsset
	// ErrNoRoute is the error a plugin's ErrorHook receives when no route matched the
	// request. It is deliberately not ErrNotFound: both produce a 404, and a plugin
	// asking why needs to tell an unmatched URL (a routing or link problem) from a
	// missing record (a content one).
	ErrNoRoute = httpx.ErrNoRoute
	// ErrNotFound reports that a fragment's data does not exist, as distinct from a
	// failure to fetch it. It is the one sentinel here that application code returns
	// rather than receives: a data handler that wraps it — fmt.Errorf("...: %w",
	// collage.ErrNotFound) — makes a Required fragment's failure render the page's
	// NotFoundPage with a 404 instead of its ErrorPage with a 500. Without wrapping
	// it, every missing record is a 500 and a page-specific 404 page is unreachable.
	ErrNotFound = types.ErrNotFound
	// ErrPageNotFound is returned by App.RenderPath when a path resolves to no page.
	ErrPageNotFound = core.ErrPageNotFound
	// ErrGuardRefused is returned by RenderFragment when the fragment's guard
	// refuses the reader: the plugin sends that reader nothing.
	ErrGuardRefused = types.ErrGuardRefused
)

// Rendering.
var (
	// ErrTemplateNotFound is returned when a page's fragment names a template the engine
	// has not loaded — at registration, and again at render time if one is somehow
	// reached.
	ErrTemplateNotFound = core.ErrTemplateNotFound
	// ErrTemplateType matches every TemplateTypeError.
	ErrTemplateType = types.ErrTemplateType
	// ErrMaxDepthExceeded is the render failure reported when a fragment tree nests
	// deeper than the engine allows, which almost always means a fragment was bound,
	// directly or indirectly, into one of its own slots.
	ErrMaxDepthExceeded = render.ErrMaxDepthExceeded
	// ErrRequiredSlotEmpty is the render failure reported when a fragment declares a
	// slot Required and nothing is bound to it. It is distinct from
	// ErrRequiredSlotUnfilled, which reports the same condition at validation time: this
	// one is a per-render failure subject to the fragment failure policy.
	ErrRequiredSlotEmpty = render.ErrRequiredSlotEmpty
	// ErrRequiredSlotUnfilled is returned when a fragment declares a slot as Required
	// but the slot has no fill.
	ErrRequiredSlotUnfilled = types.ErrRequiredSlotUnfilled
	// ErrSlotResolved is recorded when one slot is given both a resolver and bound
	// fragments.
	ErrSlotResolved = types.ErrSlotResolved
	// ErrFragmentCycle is returned when a fragment tree contains a fragment reachable
	// from itself.
	ErrFragmentCycle = types.ErrFragmentCycle
	// ErrEmptyErrorPage is the error a plugin's ErrorHook receives, under the stage
	// "error_page", when a registered error page rendered successfully but produced no
	// markup. It is the failure nobody finds out about otherwise, because the client
	// still receives a plausible-looking built-in page.
	ErrEmptyErrorPage = httpx.ErrEmptyErrorPage
	// ErrPanic is the error a plugin's ErrorHook receives, under the stage "panic", when
	// something panicked while serving a request and the framework recovered it into a
	// 500. It covers a panic anywhere the request touches code the framework did not
	// write — a Router, Cache, Metrics, or Tracer implementation, or a plugin hook — but
	// not one inside a data handler or a template function, which the render engine
	// recovers itself as a PanicError.
	//
	// The error wraps a *PanicError as well, so errors.As reaches the panic value and
	// the stack it was raised on, and errors.Is reaches the value when it was an error.
	ErrPanic = httpx.ErrPanic
	// ErrAssetFailed is the error a plugin's ErrorHook receives, under the stage
	// "asset", when a mounted asset request completes with a status of 400 or above.
	// It is one sentinel for every such status — a 404 for a missing file and a 405
	// for a disallowed method both report it — rather than one per code, since a
	// plugin reacting to "this asset request failed" needs no finer distinction than
	// that. A panic recovered while serving a mount is reported as ErrPanic instead.
	ErrAssetFailed = httpx.ErrAssetFailed
	// ErrHandlerFailed is reported to error hooks when a handler mounted with
	// App.Handle answers with a server error.
	ErrHandlerFailed = httpx.ErrHandlerFailed
	// ErrVaryTooLate is returned by Vary once the request's cache key has been
	// computed. Declare dimensions from middleware.
	ErrVaryTooLate = httpx.ErrVaryTooLate
	// ErrVaryOutsideRequest is returned by Vary for a request no collage handler is
	// serving.
	ErrVaryOutsideRequest = httpx.ErrVaryOutsideRequest
	// ErrCachedFetchPanicked is what a Cached call waiting on another's fetch is
	// told when that fetch panicked. The panic goes to the render whose fetch it
	// was, and the key is free: the next call fetches again.
	ErrCachedFetchPanicked = datacache.ErrFetchPanicked
)

// Documents.
var (
	// ErrNilDocument is returned by RegisterDocument when passed a nil document.
	ErrNilDocument = types.ErrNilDocument
	// ErrEmptyContentType is returned when a document declares no content type. A
	// document's content type is static and required: the framework writes it on every
	// response and never guesses it.
	ErrEmptyContentType = types.ErrEmptyContentType
	// ErrNoDocumentHandler is returned when a document declares neither a handler nor a
	// body. Unlike a page, a document has no template to fall back on, so it must have
	// one or the other.
	ErrNoDocumentHandler = types.ErrNoDocumentHandler
	// ErrDuplicateDocument is returned when a document is registered under a name
	// another document already holds.
	ErrDuplicateDocument = core.ErrDuplicateDocument
	// ErrDocumentNotFound is returned by App.RenderDocumentPath when a path resolves to
	// no document — including when it resolves to a page or a redirect instead.
	ErrDocumentNotFound = core.ErrDocumentNotFound
	// ErrEmptyDocumentBody is recorded in BuildReport.Errors, and no file is written,
	// when a document renders successfully but produces no body — the same condition
	// internal/httpx serves as a 500 for a live request, reused here (from
	// internal/types, where it lives alongside ErrNotFound as a property of a
	// document's handler contract rather than of HTTP serving or of static building)
	// rather than a second sentinel for the same failure mode. See
	// DocumentHandlerFunc: a handler that genuinely wants to serve an empty document
	// can return a single newline.
	ErrEmptyDocumentBody = types.ErrEmptyDocumentBody
)

// Static build.
var (
	// ErrBuildFindings is in a build's errors when a plugin reported an error-level
	// finding. The pages were written; what was found is in BuildReport.Findings.
	ErrBuildFindings = build.ErrFindings
	// ErrDuplicateRedirect is in a build's errors when two redirects — pages',
	// documents' or RedirectSource plugins' — share one From.
	ErrDuplicateRedirect = build.ErrDuplicateRedirect
	// ErrRedirectShadowsFile is in a build's errors when a redirect's From is the
	// path of a file the build wrote ("/a" or "/a/" for a directory's index.html).
	ErrRedirectShadowsFile = build.ErrRedirectShadowsFile
	// ErrOutputPathCollision is returned by Build when two pages would be written to
	// the same file — two patterns differing only in a trailing slash, or
	// StaticParams listing one set of values twice. It is reported before anything renders, so
	// a collision costs no work and leaves no half-built output directory.
	ErrOutputPathCollision = build.ErrOutputPathCollision
	// ErrEmptyOutDir is returned by Build when BuildOptions.OutDir is empty.
	ErrEmptyOutDir = build.ErrInvalidOutDir
	// ErrDangerousOutDir is returned by Build when BuildOptions.OutDir
	// resolves — after symlinks are followed — to a filesystem root, which Build
	// refuses to write into at all, or when BuildOptions.Clean is true and OutDir
	// additionally resolves to a repository root.
	ErrDangerousOutDir = build.ErrDangerousOutDir
	// ErrPathEscapesOutDir is returned when a resolved output path falls outside
	// BuildOptions.OutDir, including when a component of the path is a symlink
	// that would otherwise carry the write outside it.
	ErrPathEscapesOutDir = build.ErrPathEscapesOutDir
	// ErrDynamicPathUnresolved is recorded, as SkipRecord.Err and in
	// SkipRecord.Reason, when a page's or document's path pattern for a locale
	// contains a "{param}" segment and it was built without WithStaticParams.
	ErrDynamicPathUnresolved = build.ErrDynamicPathUnresolved
	// ErrDuplicateOutputPath is recorded, as SkipRecord.Err and in SkipRecord.Reason,
	// when two document build tasks resolve to the same output file —
	// StaticParams listing one set of values twice. One pattern in two locales is not
	// a collision: a non-default locale's document is written under its prefix,
	// "/tr/sitemap.xml". One task is built and the rest are skipped by name, rather
	// than racing to overwrite one file.
	ErrDuplicateOutputPath = build.ErrDuplicateOutputPath
	// ErrDegradedRender is recorded in BuildReport.Errors, and no file is written,
	// when a page renders with at least one failed fragment and
	// BuildOptions.AllowDegraded is false.
	ErrDegradedRender = build.ErrDegradedRender
	// ErrEmptyRender is recorded in BuildReport.Errors, and no file is written, when
	// a page renders successfully but produces no markup at all. It is refused
	// regardless of BuildOptions.AllowDegraded.
	ErrEmptyRender = build.ErrEmptyRender
	// ErrUnresolvedToken is recorded in BuildReport.Errors when the not-found page
	// rendered for a static build still contains a request-forgery token placeholder. A
	// built site has no server to replace it with a reader's own token, and no server to
	// submit the form to. Any other page carrying one is not written either, but is
	// recorded in BuildReport.Skipped: it is served rather than exported.
	ErrUnresolvedToken = build.ErrUnresolvedToken
	// ErrBuildPanic is recorded in BuildReport.Errors when rendering or writing one
	// page or document panicked. The build recovers it, records it against that page
	// or document, and continues with the rest.
	ErrBuildPanic = build.ErrBuildPanic
	// ErrNotStatic is a SkipRecord's Err for a page or document declared Dynamic(),
	// which a static build does not write.
	ErrNotStatic = build.ErrNotStatic
	// ErrGuarded is a SkipRecord's Err for a page whose layout chain or content
	// fragment carries a guard, which a static build does not write: a file on a
	// static host has no guard in front of it.
	ErrGuarded = build.ErrGuarded
)

// Plugins.
var (
	// ErrDuplicatePlugin is returned by App.RegisterPlugin when a plugin with the same
	// Name is already registered.
	ErrDuplicatePlugin = plugin.ErrDuplicatePlugin
	// ErrDuplicateTemplateFunc is returned by ConfigHost.AddTemplateFunc when another
	// plugin already registered that name.
	ErrDuplicateTemplateFunc = plugin.ErrDuplicateTemplateFunc
	// ErrUnknownPluginConfig is returned when the application starts — by Start,
	// Handler, ListenAndServe or a render, not by New, because a plugin can still be
	// registered in between — if Config.PluginConfig holds a key
	// matching no registered plugin — the typo case, which would otherwise leave a
	// plugin running on defaults and an operator certain it was configured.
	ErrUnknownPluginConfig = plugin.ErrUnknownPluginConfig
)
