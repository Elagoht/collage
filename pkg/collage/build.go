package collage

import (
	"github.com/Elagoht/collage/internal/build"
	"github.com/Elagoht/collage/internal/types"
)

// Builder renders an application's static-eligible pages and documents to files
// under BuildOptions.OutDir, and copies its mounted files beside them. Construct
// one with NewBuilder.
type Builder = build.Builder

// BuildOptions configures a static build. See NewBuilder.
type BuildOptions = build.Options

// BuildReport summarizes the outcome of a Builder.Build call.
type BuildReport = build.Report

// SkipRecord describes one page or document, or one locale of one, that a static
// build could not produce, and why.
type SkipRecord = build.SkipRecord

// WarningRecord describes a page or document a static build wrote, but not all
// of: one whose content depends on a query string. See BuildReport.Warnings.
type WarningRecord = build.WarningRecord

// ErrOutputPathCollision is returned by Build when two pages would be written to
// the same file — two patterns differing only in a trailing slash, or
// StaticParams listing one set of values twice. It is reported before anything renders, so
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

// ErrDynamicPathUnresolved is recorded, as SkipRecord.Err and in
// SkipRecord.Reason, when a page's or document's path pattern for a locale
// contains a "{param}" segment and it was built without WithStaticParams.
var ErrDynamicPathUnresolved = build.ErrDynamicPathUnresolved

// ErrDuplicateOutputPath is recorded, as SkipRecord.Err and in SkipRecord.Reason,
// when two document build tasks resolve to the same output file —
// StaticParams listing one set of values twice. One pattern in two locales is not
// a collision: a non-default locale's document is written under its prefix,
// "/tr/sitemap.xml". One task is built and the rest are skipped by name, rather
// than racing to overwrite one file.
var ErrDuplicateOutputPath = build.ErrDuplicateOutputPath

// ErrDegradedRender is recorded in BuildReport.Errors, and no file is written,
// when a page renders with at least one failed fragment and
// BuildOptions.AllowDegraded is false.
var ErrDegradedRender = build.ErrDegradedRender

// ErrEmptyRender is recorded in BuildReport.Errors, and no file is written, when
// a page renders successfully but produces no markup at all. It is refused
// regardless of BuildOptions.AllowDegraded.
var ErrEmptyRender = build.ErrEmptyRender

// ErrUnresolvedToken is recorded in BuildReport.Errors when the not-found page
// rendered for a static build still contains a request-forgery token placeholder. A
// built site has no server to replace it with a reader's own token, and no server to
// submit the form to. Any other page carrying one is not written either, but is
// recorded in BuildReport.Skipped: it is served rather than exported.
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

// ErrNotStatic is a SkipRecord's Err for a page or document declared Dynamic(),
// which a static build does not write.
var ErrNotStatic = build.ErrNotStatic
