// Package build renders a collage application's cacheable pages to static files on
// disk. It consumes the application through a narrow Renderer interface — Pages plus
// a render entry point — rather than importing internal/core's concrete type, so the
// dependency points from core toward build and not the other way, and so Builder is
// testable against a fake.
//
// Build renders every static-eligible page through the same render engine the HTTP
// server uses (Renderer.RenderPath), so the files this package writes are
// byte-identical to what a live request would produce.
package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/types"
)

// ErrNilRenderer is returned by New when app is nil.
var ErrNilRenderer = errors.New("collage: nil renderer")

// ErrInvalidOutDir is returned by New when Options.OutDir is empty.
var ErrInvalidOutDir = errors.New("collage: invalid output directory")

// ErrDangerousCleanTarget is returned by Build when Options.Clean is true and
// Options.OutDir resolves to a filesystem root or a repository root — a directory
// whose contents must never be silently deleted because a config field was blank or
// mistyped.
var ErrDangerousCleanTarget = errors.New("collage: refusing to clean a dangerous output directory")

// ErrPathEscapesOutDir is returned when a resolved output path falls outside
// Options.OutDir. PathProvider is user code: a provider returning "../escape", or a
// path built from an unsanitised parameter, must not be able to write outside the
// configured output directory.
var ErrPathEscapesOutDir = errors.New("collage: resolved path escapes the output directory")

// ErrDynamicPathUnresolved is recorded, as a SkipRecord.Reason, when a page's path
// pattern for a locale contains a "{param}" segment and Options.PathProvider is nil.
// Such a page has no way to enumerate the concrete paths a static build must write.
var ErrDynamicPathUnresolved = errors.New("collage: dynamic path pattern requires a path provider")

// Renderer is the narrow surface Builder needs from an application: the registered
// pages, and a way to render one of them by path outside the HTTP request path. It
// is declared here, rather than imported from internal/core, so this package can be
// tested against a fake and so the dependency between the two packages points from
// core to build.
//
// *core.App satisfies Renderer.
type Renderer interface {
	// Pages returns every registered page.
	Pages() []*types.Page
	// RenderPath renders the page registered at path for locale, overlaying params
	// onto whatever path parameters the router itself captures, and bypasses any
	// render cache.
	RenderPath(ctx context.Context, path, locale string, params map[string]string) (*render.Result, error)
}

// PathProvider supplies the concrete paths a dynamic page's pattern expands to. A
// page whose path pattern for a locale contains a "{param}" (or "{param...}")
// segment cannot be built statically without one.
type PathProvider interface {
	// Paths returns every concrete path a static build should render page at, for
	// locale. Path values are user-supplied and are validated against
	// Options.OutDir before anything is written; see ErrPathEscapesOutDir.
	Paths(ctx context.Context, page *types.Page, locale string) ([]PathInstance, error)
}

// PathInstance is one concrete URL a dynamic page is built for, plus the path
// parameter values that reached it — passed through to Renderer.RenderPath so a
// fragment's data handler sees the same parameters a live request would have
// captured from the URL.
type PathInstance struct {
	// Path is the concrete request path to render, e.g. "/blog/hello-world".
	Path string
	// Params overlays the path parameter values a live request would have
	// captured for Path, keyed by placeholder name.
	Params map[string]string
}

// Options configures a static build.
type Options struct {
	// OutDir is the directory static output is written under. It must be
	// non-empty; New returns ErrInvalidOutDir otherwise.
	OutDir string
	// Locales restricts the build to these locales. An empty Locales builds every
	// locale each page declares in its own Paths.
	Locales []string
	// Clean removes OutDir's existing contents (not OutDir itself) before writing.
	// Build refuses to do so when OutDir resolves to a filesystem root or a
	// repository root; see ErrDangerousCleanTarget.
	Clean bool
	// Concurrency bounds how many pages render and write concurrently. Zero or
	// negative defaults to 1 — sequential, matching the framework's determinism
	// invariant. Report contents are deterministic regardless of this value: tasks
	// are always merged back into their original enumeration order.
	Concurrency int
	// PathProvider supplies concrete paths for pages whose pattern contains a
	// "{param}" segment. A dynamic page with no PathProvider is recorded as
	// skipped rather than failing the build.
	PathProvider PathProvider
}

// SkipRecord describes one page, or one page's locale, that a static build could not
// produce, and why.
type SkipRecord struct {
	// Page is the skipped page's Name.
	Page string
	// Locale is the specific locale that was skipped. It is empty when the whole
	// page was skipped regardless of locale (a non-cacheable render strategy).
	Locale string
	// Reason explains why the page, or the page's locale, was skipped.
	Reason string
}

// Report summarizes the outcome of a Build call.
type Report struct {
	// Written lists the absolute filesystem paths that were written, one per
	// rendered page, locale, and concrete path.
	Written []string
	// Skipped lists every page, or page locale, the build could not produce
	// statically.
	Skipped []SkipRecord
	// Errors lists every render, path-resolution, or write failure encountered.
	// Build's returned error is errors.Join of exactly these, so a caller that
	// wants the individual failures can read them here instead of unwrapping the
	// joined error.
	Errors []error
	// Duration is how long the Build call took, from entry to return.
	Duration time.Duration
}

// Builder renders an application's static-eligible pages to files under
// Options.OutDir.
type Builder struct {
	app  Renderer
	opts Options
}

// New constructs a Builder that renders app's pages according to opts. It returns
// ErrNilRenderer when app is nil and ErrInvalidOutDir when opts.OutDir is empty. A
// non-positive opts.Concurrency is defaulted to 1.
func New(app Renderer, opts Options) (*Builder, error) {
	if app == nil {
		return nil, ErrNilRenderer
	}
	if opts.OutDir == "" {
		return nil, ErrInvalidOutDir
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 1
	}
	return &Builder{app: app, opts: opts}, nil
}

// buildTask is one page, locale, and concrete path to render and write.
type buildTask struct {
	page   *types.Page
	locale string
	path   string
	params map[string]string
}

// Build renders every static-eligible page of the application to files under
// Options.OutDir and returns a Report describing what happened.
//
// A page using a non-cacheable render strategy is skipped, recorded in the report,
// rather than built. A page whose path pattern for a locale contains a "{param}"
// segment is expanded through Options.PathProvider when one is configured, or
// skipped with ErrDynamicPathUnresolved otherwise. A failure resolving paths,
// rendering, or writing one page does not stop the rest of the build: every such
// failure is recorded in Report.Errors, and the error Build returns is
// errors.Join(report.Errors...) — nil when there were none.
//
// Build writes each rendered page to "<OutDir>/<path>/index.html", creating
// directories as needed; the root path "/" writes "<OutDir>/index.html". Every
// resolved output path is verified to stay within OutDir — see ErrPathEscapesOutDir
// — since PathProvider is user code and a path built from an unsanitised parameter
// must not be able to write outside the output directory.
//
// Options.Concurrency bounds how many pages render and write at once, but
// Report.Written, Report.Skipped, and Report.Errors are always assembled in the same
// order Build enumerated pages in, regardless of that concurrency: this package's
// output is deterministic by construction, not only at the default concurrency of 1.
func (b *Builder) Build(ctx context.Context) (*Report, error) {
	start := time.Now()
	report := &Report{}

	outDirResolved, err := b.prepareOutDir()
	if err != nil {
		report.Duration = time.Since(start)
		return report, err
	}

	tasks, skipped, enumerateErrs := b.enumerate(ctx)
	report.Skipped = skipped

	written := make([]string, len(tasks))
	taskErrs := make([]error, len(tasks))

	sem := make(chan struct{}, b.opts.Concurrency)
	var wg sync.WaitGroup
	for i, task := range tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, task buildTask) {
			defer wg.Done()
			defer func() { <-sem }()
			target, err := b.renderAndWrite(ctx, outDirResolved, task)
			if err != nil {
				taskErrs[i] = err
				return
			}
			written[i] = target
		}(i, task)
	}
	wg.Wait()

	errs := append([]error(nil), enumerateErrs...)
	for i := range tasks {
		if taskErrs[i] != nil {
			errs = append(errs, taskErrs[i])
			continue
		}
		report.Written = append(report.Written, written[i])
	}
	report.Errors = errs
	report.Duration = time.Since(start)

	if len(errs) == 0 {
		return report, nil
	}
	return report, errors.Join(errs...)
}

// enumerate walks every registered page and expands it into the concrete build
// tasks Build must render, alongside the pages and locales that were skipped and
// the errors that came from resolving a dynamic page's paths. It never renders or
// writes anything itself.
func (b *Builder) enumerate(ctx context.Context) ([]buildTask, []SkipRecord, []error) {
	var tasks []buildTask
	var skipped []SkipRecord
	var errs []error

	allowedLocales := make(map[string]struct{}, len(b.opts.Locales))
	for _, locale := range b.opts.Locales {
		allowedLocales[locale] = struct{}{}
	}
	restrictLocales := len(allowedLocales) > 0

	for _, page := range b.app.Pages() {
		if !page.Strategy.Cacheable() {
			skipped = append(skipped, SkipRecord{
				Page:   page.Name,
				Reason: fmt.Sprintf("page uses the %s render strategy, which cannot be built statically", page.Strategy),
			})
			continue
		}

		for _, locale := range page.Locales() {
			if restrictLocales {
				if _, ok := allowedLocales[locale]; !ok {
					continue
				}
			}

			pattern, ok := page.PathFor(locale)
			if !ok {
				continue
			}

			if !isDynamicPattern(pattern) {
				tasks = append(tasks, buildTask{page: page, locale: locale, path: pattern})
				continue
			}

			if b.opts.PathProvider == nil {
				skipped = append(skipped, SkipRecord{
					Page:   page.Name,
					Locale: locale,
					Reason: ErrDynamicPathUnresolved.Error(),
				})
				continue
			}

			instances, err := b.opts.PathProvider.Paths(ctx, page, locale)
			if err != nil {
				errs = append(errs, fmt.Errorf("collage: resolve paths for page %q locale %q: %w", page.Name, locale, err))
				continue
			}
			for _, instance := range instances {
				tasks = append(tasks, buildTask{
					page:   page,
					locale: locale,
					path:   instance.Path,
					params: instance.Params,
				})
			}
		}
	}

	return tasks, skipped, errs
}

// isDynamicPattern reports whether pattern contains a "{param}" or "{param...}"
// placeholder segment. Every such segment is wrapped in braces, so a substring check
// for "{" is sufficient and does not require depending on internal/router's pattern
// parser.
func isDynamicPattern(pattern string) bool {
	return strings.Contains(pattern, "{")
}

// renderAndWrite renders one task through the application and writes the result
// under outDirResolved, which must already be an absolute, symlink-resolved
// directory (see prepareOutDir). It returns the absolute path written.
func (b *Builder) renderAndWrite(ctx context.Context, outDirResolved string, task buildTask) (string, error) {
	target, err := resolveTarget(outDirResolved, task.path)
	if err != nil {
		return "", fmt.Errorf("collage: page %q locale %q: %w", task.page.Name, task.locale, err)
	}

	result, err := b.app.RenderPath(ctx, task.path, task.locale, task.params)
	if err != nil {
		return "", fmt.Errorf("collage: render page %q locale %q path %q: %w", task.page.Name, task.locale, task.path, err)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("collage: create directory for %q: %w", target, err)
	}
	if err := os.WriteFile(target, result.HTML, 0o644); err != nil {
		return "", fmt.Errorf("collage: write %q: %w", target, err)
	}
	return target, nil
}

// resolveTarget turns urlPath into the file it is written to under outDirResolved
// ("<outDirResolved>/<urlPath>/index.html", or "<outDirResolved>/index.html" for the
// root path "/"), and verifies the result stays inside outDirResolved.
//
// Containment is decided with filepath.Rel plus a check that the result does not
// start with "..", not a string-prefix check: a prefix check would let
// "/outsibling" pass a check meant to require containment inside "/out".
// outDirResolved must already be filepath.EvalSymlinks-resolved (see
// prepareOutDir) so a symlinked OutDir — /tmp on macOS resolves to /private/tmp,
// which t.TempDir() itself returns — does not make every legitimate write look
// like an escape.
func resolveTarget(outDirResolved, urlPath string) (string, error) {
	trimmed := strings.TrimPrefix(urlPath, "/")
	rel := filepath.FromSlash(trimmed)

	var target string
	if rel == "" || rel == "." {
		target = filepath.Join(outDirResolved, "index.html")
	} else {
		target = filepath.Join(outDirResolved, rel, "index.html")
	}

	withinRel, err := filepath.Rel(outDirResolved, target)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrPathEscapesOutDir, urlPath)
	}
	if withinRel == ".." || strings.HasPrefix(withinRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrPathEscapesOutDir, urlPath)
	}
	return target, nil
}

// prepareOutDir resolves Options.OutDir to an absolute, symlink-resolved directory,
// optionally cleaning its contents first, and returns that resolved path for the
// rest of Build to write under and check escapes against.
func (b *Builder) prepareOutDir() (string, error) {
	absOutDir, err := filepath.Abs(b.opts.OutDir)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidOutDir, err)
	}
	cleanOutDir := filepath.Clean(absOutDir)

	if b.opts.Clean {
		if dangerousOutDir(cleanOutDir) {
			return "", fmt.Errorf("%w: %s", ErrDangerousCleanTarget, cleanOutDir)
		}
		if err := cleanDirContents(cleanOutDir); err != nil {
			return "", fmt.Errorf("collage: clean %q: %w", cleanOutDir, err)
		}
	}

	if err := os.MkdirAll(cleanOutDir, 0o755); err != nil {
		return "", fmt.Errorf("collage: create output directory %q: %w", cleanOutDir, err)
	}

	// Resolved once, here, rather than per written file: a symlinked OutDir (macOS
	// /tmp -> /private/tmp, which t.TempDir() itself returns) must not make every
	// subsequent containment check in resolveTarget reject a legitimate write.
	resolved, err := filepath.EvalSymlinks(cleanOutDir)
	if err != nil {
		return "", fmt.Errorf("collage: resolve output directory %q: %w", cleanOutDir, err)
	}
	return resolved, nil
}

// cleanDirContents removes every entry inside dir, leaving dir itself in place. A
// dir that does not exist yet is not an error: there is nothing to clean.
func cleanDirContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// dangerousOutDir reports whether dir — already absolute and filepath.Clean-ed — is
// a directory whose contents Build must never be asked to delete: a filesystem root,
// or a repository root. Options.OutDir being empty is already rejected by New, for
// every build, not only a cleaning one, so it is not re-checked here.
func dangerousOutDir(dir string) bool {
	// filepath.Dir of a filesystem root returns the root itself; no other
	// directory is its own parent, on Unix or Windows.
	if filepath.Dir(dir) == dir {
		return true
	}
	return hasRepositoryMarker(dir)
}

// hasRepositoryMarker reports whether dir looks like the root of a source
// repository: it directly contains a "go.mod" or a ".git". Deleting a user's working
// tree because Options.OutDir was left pointing at it by mistake is unacceptable, and
// this project has no other stdlib-only way to recognise "the repository root"
// without shelling out to git.
func hasRepositoryMarker(dir string) bool {
	for _, marker := range []string{"go.mod", ".git"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}
