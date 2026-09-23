package collage

import (
	"github.com/Elagoht/collage/internal/build"
	"github.com/Elagoht/collage/internal/types"
)

// Builder renders an application's static-eligible pages to files under
// BuildOptions.OutDir. Construct one with NewBuilder.
type Builder = build.Builder

// BuildOptions configures a static build. See NewBuilder.
type BuildOptions = build.Options

// BuildReport summarizes the outcome of a Builder.Build call.
type BuildReport = build.Report

// PathProvider supplies the concrete paths a dynamic page's pattern expands
// to, for BuildOptions.PathProvider. A page whose path pattern for a locale
// contains a "{param}" (or "{param...}") segment cannot be built statically
// without one.
type PathProvider = build.PathProvider

// DocumentPathProvider supplies the concrete paths a dynamic document's
// pattern expands to, for BuildOptions.DocumentPathProvider. It is
// PathProvider's sibling for documents, kept as its own interface rather than
// a widening of PathProvider so an existing PathProvider implementation keeps
// compiling.
type DocumentPathProvider = build.DocumentPathProvider

// PathInstance is one concrete URL a dynamic page is built for, plus the path
// parameter values that reached it.
type PathInstance = build.PathInstance

// SkipRecord describes one page, or one page's locale, that a static build
// could not produce, and why.
type SkipRecord = build.SkipRecord

// ErrOutputPathCollision is returned by Build when two pages would be written to
// the same file — two patterns differing only in a trailing slash, or a
// PathProvider returning one path twice. It is reported before anything renders, so
// a collision costs no work and leaves no half-built output directory.
var ErrOutputPathCollision = build.ErrOutputPathCollision

// ErrNilRenderer is returned by NewBuilder when app is nil.
var ErrNilRenderer = build.ErrNilRenderer

// ErrInvalidOutDir is returned by NewBuilder when BuildOptions.OutDir is
// empty.
var ErrInvalidOutDir = build.ErrInvalidOutDir

// ErrDangerousOutDir is returned by Builder.Build when BuildOptions.OutDir
// resolves — after symlinks are followed — to a filesystem root, which Build
// refuses to write into at all, or when BuildOptions.Clean is true and OutDir
// additionally resolves to a repository root.
var ErrDangerousOutDir = build.ErrDangerousOutDir

// ErrPathEscapesOutDir is returned when a resolved output path falls outside
// BuildOptions.OutDir, including when a component of the path is a symlink
// that would otherwise carry the write outside it.
var ErrPathEscapesOutDir = build.ErrPathEscapesOutDir

// ErrDynamicPathUnresolved is recorded, as a SkipRecord.Reason, when a page's
// path pattern for a locale contains a "{param}" segment and
// BuildOptions.PathProvider is nil.
var ErrDynamicPathUnresolved = build.ErrDynamicPathUnresolved

// ErrDuplicateOutputPath is recorded, as a SkipRecord.Reason, when two document
// build tasks resolve to the same output file — one document registered at the
// same pattern under two locales, which is the form that serves both
// "/sitemap.xml" and "/tr/sitemap.xml". One task is built and the rest are
// skipped by name, rather than racing to overwrite one file.
var ErrDuplicateOutputPath = build.ErrDuplicateOutputPath

// ErrDegradedRender is recorded in BuildReport.Errors, and no file is written,
// when a page renders with at least one failed fragment and
// BuildOptions.AllowDegraded is false.
var ErrDegradedRender = build.ErrDegradedRender

// ErrEmptyRender is recorded in BuildReport.Errors, and no file is written, when
// a page renders successfully but produces no markup at all. It is refused
// regardless of BuildOptions.AllowDegraded.
var ErrEmptyRender = build.ErrEmptyRender

// ErrUnresolvedToken is recorded in BuildReport.Errors when a page rendered for a
// static build still contains a request-forgery token placeholder. A built site has
// no server to replace it with a reader's own token, and no server to submit the
// form to; declare such a page Dynamic() so the build skips it.
var ErrUnresolvedToken = build.ErrUnresolvedToken

// ErrBuildPanic is recorded in BuildReport.Errors when rendering or writing one
// page or document panicked. The build recovers it, records it against that page
// or document, and continues with the rest.
var ErrBuildPanic = build.ErrBuildPanic

// ErrEmptyDocumentBody is recorded in BuildReport.Errors, and no file is written,
// when a document renders successfully but produces no body — the same condition
// internal/httpx serves as a 500 for a live request, reused here (from
// internal/types, where it lives alongside ErrNotFound as a property of a
// document's handler contract rather than of HTTP serving or of static building)
// rather than a second sentinel for the same failure mode. See
// DocumentHandlerFunc: a handler that genuinely wants to serve an empty document
// can return a single newline.
var ErrEmptyDocumentBody = types.ErrEmptyDocumentBody

// NewBuilder returns a static-site builder that renders app's pages and
// documents, and copies app's mounted assets, according to opts, ready for
// Build. It returns ErrNilRenderer when app is nil and ErrInvalidOutDir when
// opts.OutDir is empty.
//
// This is a thin wrapper, not a second implementation: *App (an alias for
// *internal/core.App) already satisfies internal/build's own narrow Renderer
// interface — Pages, RenderPath, Documents, RenderDocumentPath and Mounts —
// so NewBuilder does nothing beyond handing app to build.New. Every safety
// check Build performs (symlink-escape containment, the dangerous-output-
// directory refusals) is internal/build's own, not reimplemented here. Which
// mounts a build copies is answered entirely by app.Mounts(), reached through
// Renderer, not by anything on BuildOptions: a caller cannot hand New one
// application's pages alongside a different application's mounts, because
// there is no field through which to do so.
//
// The nil check here is not redundant with build.New's own: app arrives as
// the concrete *App, and handing a nil *App to build.New's Renderer parameter
// would box it into a non-nil interface value with a nil underlying pointer —
// which build.New's own "app == nil" check cannot see through. Checking the
// concrete pointer here, before that boxing happens, is what makes
// ErrNilRenderer actually reachable through this wrapper.
func NewBuilder(app *App, opts BuildOptions) (*Builder, error) {
	if app == nil {
		return nil, ErrNilRenderer
	}
	return build.New(app, opts)
}
