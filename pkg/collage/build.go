package collage

import (
	"context"
	"time"

	"github.com/Elagoht/collage/internal/build"
	"github.com/Elagoht/collage/internal/types"
)

// BuildOptions configures a static build. See Build.
type BuildOptions struct {
	// OutDir is the directory the build writes under. It must not be empty;
	// Build returns ErrEmptyOutDir otherwise.
	OutDir string
	// Locales restricts the build to these locales. An empty Locales builds every
	// locale each page declares in its own paths.
	Locales []string
	// Clean removes OutDir's existing contents (not OutDir itself) before writing.
	// Build refuses to run at all when OutDir resolves to a filesystem root, and
	// refuses to clean (though it still writes) when OutDir resolves to a
	// repository root; see ErrDangerousOutDir.
	Clean bool
	// Concurrency bounds how many pages render and write at once. Zero or negative
	// means 1. The report is the same whatever the value: results are merged back
	// in registration order.
	Concurrency int
	// AllowDegraded writes a page whose render had at least one failed fragment
	// instead of recording ErrDegradedRender against it. It is off by default: a
	// file has no TTL, so a degraded page written to disk stays degraded until the
	// next build. Either way the failures are in BuildReport.Errors, and a render
	// that produced no markup at all is still refused, with ErrEmptyRender.
	AllowDegraded bool
}

// internal converts o to the build engine's options, field by field.
func (o BuildOptions) internal() build.Options {
	return build.Options{
		OutDir:        o.OutDir,
		Locales:       o.Locales,
		Clean:         o.Clean,
		Concurrency:   o.Concurrency,
		AllowDegraded: o.AllowDegraded,
	}
}

// BuildReport summarizes the outcome of a Build call.
type BuildReport struct {
	// Written lists the absolute paths of the files written: one per page or
	// document, plus one per copied mounted file.
	Written []string
	// Skipped lists every page or document, or locale of one, the build could not
	// produce statically.
	Skipped []SkipRecord
	// Warnings lists every page or document that was written but is not the whole
	// of what the served one is, such as one whose content depends on a query
	// string, which a file cannot carry.
	Warnings []WarningRecord
	// Findings lists what plugins checking the output reported, about each page
	// and about the build as a whole. An error-level finding also puts
	// ErrBuildFindings in Errors.
	Findings []Finding
	// Errors lists every render, path-resolution or write failure. Build's
	// returned error is errors.Join of exactly these.
	Errors []error
	// Duration is how long the Build call took.
	Duration time.Duration
}

// SkipRecord describes one page or document, or one locale of one, that a static
// build could not produce, and why.
type SkipRecord struct {
	// Name is the skipped page's or document's name.
	Name string
	// Locale is the locale that was skipped, or "" when the whole page or document
	// was skipped regardless of locale.
	Locale string
	// Reason explains why it was skipped.
	Reason string
	// Err is the sentinel for the reason — ErrNotStatic, ErrGuarded,
	// ErrDynamicPathUnresolved, ErrUnresolvedToken, ErrDuplicateOutputPath — for a
	// caller that acts on the kind of skip rather than reading the sentence.
	Err error
}

// WarningRecord describes a page or document a static build wrote, but not all
// of: one whose content depends on a query string. See BuildReport.Warnings.
type WarningRecord struct {
	// Name is the page's or document's name.
	Name string
	// Reason explains what the written file does not contain.
	Reason string
}

// reportFrom converts the build engine's report, field by field. Nil stays nil,
// for the report and for each of its slices.
func reportFrom(r *build.Report) *BuildReport {
	if r == nil {
		return nil
	}
	out := &BuildReport{
		Written:  r.Written,
		Findings: r.Findings,
		Errors:   r.Errors,
		Duration: r.Duration,
	}
	if r.Skipped != nil {
		out.Skipped = make([]SkipRecord, len(r.Skipped))
		for i, s := range r.Skipped {
			out.Skipped[i] = SkipRecord{Name: s.Name, Locale: s.Locale, Reason: s.Reason, Err: s.Err}
		}
	}
	if r.Warnings != nil {
		out.Warnings = make([]WarningRecord, len(r.Warnings))
		for i, w := range r.Warnings {
			out.Warnings[i] = WarningRecord{Name: w.Name, Reason: w.Reason}
		}
	}
	return out
}

// ErrBuildFindings is in a build's errors when a plugin reported an error-level
// finding. The pages were written; what was found is in BuildReport.Findings.
var ErrBuildFindings = build.ErrFindings

// ErrDuplicateRedirect is in a build's errors when two redirects — pages',
// documents' or RedirectSource plugins' — share one From.
var ErrDuplicateRedirect = build.ErrDuplicateRedirect

// ErrRedirectShadowsFile is in a build's errors when a redirect's From is the
// path of a file the build wrote ("/a" or "/a/" for a directory's index.html).
var ErrRedirectShadowsFile = build.ErrRedirectShadowsFile

// ErrOutputPathCollision is returned by Build when two pages would be written to
// the same file — two patterns differing only in a trailing slash, or
// StaticParams listing one set of values twice. It is reported before anything renders, so
// a collision costs no work and leaves no half-built output directory.
var ErrOutputPathCollision = build.ErrOutputPathCollision

// ErrEmptyOutDir is returned by Build when BuildOptions.OutDir is empty.
var ErrEmptyOutDir = build.ErrInvalidOutDir

// ErrDangerousOutDir is returned by Build when BuildOptions.OutDir
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

// Build renders app's static pages and documents into opts.OutDir, copies its
// mounted files beside them, and reports what it wrote, skipped and warned about.
// It returns ErrNilApp for a nil app and ErrEmptyOutDir for an empty OutDir.
//
// The report is returned alongside the error, so a caller can print what was
// written as well as what failed. The error is errors.Join of BuildReport.Errors,
// except when the build stopped before rendering — the application failed to
// start, or OutDir was refused — where it is that failure alone.
func Build(ctx context.Context, app *App, opts BuildOptions) (*BuildReport, error) {
	if app == nil || app.c == nil {
		return nil, ErrNilApp
	}
	b, err := build.New(app.c, opts.internal())
	if err != nil {
		return nil, err
	}
	r, err := b.Build(ctx)
	return reportFrom(r), err
}

// ErrNotStatic is a SkipRecord's Err for a page or document declared Dynamic(),
// which a static build does not write.
var ErrNotStatic = build.ErrNotStatic

// ErrGuarded is a SkipRecord's Err for a page whose layout chain or content
// fragment carries a guard, which a static build does not write: a file on a
// static host has no guard in front of it.
var ErrGuarded = build.ErrGuarded
