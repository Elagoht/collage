package collage

import (
	"github.com/Elagoht/collage/internal/build"
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

// PathInstance is one concrete URL a dynamic page is built for, plus the path
// parameter values that reached it.
type PathInstance = build.PathInstance

// SkipRecord describes one page, or one page's locale, that a static build
// could not produce, and why.
type SkipRecord = build.SkipRecord

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

// NewBuilder returns a static-site builder that renders app's pages according
// to opts, ready for Build. It returns ErrNilRenderer when app is nil and
// ErrInvalidOutDir when opts.OutDir is empty.
//
// This is a thin wrapper, not a second implementation: *App (an alias for
// *internal/core.App) already satisfies internal/build's own narrow Renderer
// interface — Pages and RenderPath — so NewBuilder does nothing beyond handing
// app to build.New. Every safety check Build performs (symlink-escape
// containment, the dangerous-output-directory refusals) is internal/build's
// own, not reimplemented here.
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
