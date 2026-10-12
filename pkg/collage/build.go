package collage

import (
	"context"
	"time"

	"github.com/Elagoht/collage/internal/build"
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
	// returned error is errors.Join of exactly these, except when the build
	// stopped before rendering, where it is the one failure that stopped it —
	// which Errors also holds, last; see Build.
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
	// ErrDynamicPathUnresolved, ErrUnresolvedToken — for a
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

// Build renders app's static pages and documents into opts.OutDir, copies its
// mounted files beside them, and reports what it wrote, skipped and warned about.
// It returns ErrNilApp for a nil app and ErrEmptyOutDir for an empty OutDir.
//
// The report is returned alongside the error, so a caller can print what was
// written as well as what failed. The error is errors.Join of BuildReport.Errors,
// except when the build stopped before rendering — OutDir was refused, the
// application failed to start, two outputs collided (ErrOutputPathCollision), or
// OutDir could not be cleaned or created. Then the error is that failure alone,
// and BuildReport.Errors holds it last, after whatever resolving the routes'
// StaticParams failed with when that had run (a collision, or OutDir not cleaned
// or created).
//
// A dangerous OutDir (ErrDangerousOutDir) is refused before your plugins' Init or
// any StaticParams runs, unless your code changes OutDir while the build runs: it
// is checked again, where it then points, before it is cleaned or created, and
// refused there. Only that last stop can have touched OutDir.
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
