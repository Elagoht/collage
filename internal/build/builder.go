// Package build renders a collage application's cacheable pages to static files on
// disk. It consumes the application through a narrow Renderer interface — Pages plus
// a render entry point — rather than importing internal/core's concrete type, so the
// dependency points from core toward build and not the other way, and so Builder is
// testable against a fake.
//
// Build renders every static-eligible page through the same render engine the HTTP
// server uses, by way of Renderer.RenderPath — the same template set, the same
// fragment tree, the same data handlers, and the same walk.
//
// It does not go through the HTTP handler, but it does go through startup:
// RenderPath and RenderDocumentPath run plugin Init first, memoised, and fire
// BeforeRender and AfterRender around every page and DocumentRendered on every
// document, so what a plugin contributes to a served page it contributes to a built
// one. What does not fire is what is about a request or a cache rather than a
// render: PageResolvedHook (a build is not a request) and CacheWriteHook (a build
// writes files, not cache entries).
package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/Elagoht/collage/internal/asset"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/types"
)

// ErrNilRenderer is returned by New when app is nil.
var ErrNilRenderer = errors.New("collage: nil renderer")

// ErrInvalidOutDir is returned by New when Options.OutDir is empty.
var ErrInvalidOutDir = errors.New("collage: invalid output directory")

// ErrDangerousOutDir is returned by Build when Options.OutDir resolves — after
// symlinks are followed — to a filesystem root, which Build refuses to write into
// at all, or when Options.Clean is true and OutDir additionally resolves to a
// repository root, a directory whose contents must never be silently deleted
// because a config field was blank or mistyped.
var ErrDangerousOutDir = errors.New("collage: refusing to use a dangerous output directory")

// ErrOutputPathCollision is returned by Build when two tasks resolve to the same
// output file. Writing one over the other drops a page from the build with nothing
// in the report to show for it, which is the kind of silent loss this framework
// refuses everywhere else.
var ErrOutputPathCollision = errors.New("collage: two builds target one output path")

// ErrPathEscapesOutDir is returned when a resolved output path falls outside
// Options.OutDir. PathProvider is user code: a provider returning "../escape", or a
// path built from an unsanitised parameter, must not be able to write outside the
// configured output directory.
var ErrPathEscapesOutDir = errors.New("collage: resolved path escapes the output directory")

// ErrDynamicPathUnresolved is recorded, as SkipRecord.Err and in SkipRecord.Reason,
// when a page's path pattern for a locale contains a "{param}" segment and
// Options.PathProvider is nil — or a document's, and Options.DocumentPathProvider
// is nil. Such a route has no way to enumerate the concrete paths a static build
// must write.
var ErrDynamicPathUnresolved = errors.New("collage: dynamic path pattern requires a path provider")

// ErrDuplicateOutputPath is recorded, as SkipRecord.Err and in SkipRecord.Reason,
// when two document build tasks resolve to the same output file — a
// DocumentPathProvider handing back the same path twice, say. One pattern in two
// locales is not that: each non-default locale's document is written under its
// locale's prefix, "/sitemap.xml" and "/tr/sitemap.xml", exactly as they are
// served. Only one of the colliding tasks is built; the rest are skipped by name
// rather than racing to overwrite one file.
//
// It is a skip and not an error deliberately: the file that is written is the
// one the URL serves, and the only thing missing is a second copy of it.
var ErrDuplicateOutputPath = errors.New("collage: two build tasks write the same output path")

// ErrBuildPanic is recorded in Report.Errors when rendering or writing one page
// panicked. The build worker recovers it, records it against that page, and carries
// on with the rest: a static build is often the last step of a deploy, and letting
// one page's panic take the process down leaves an output directory that Clean has
// already emptied.
var ErrBuildPanic = errors.New("collage: panic while building a page")

// ErrDegradedRender is recorded in Report.Errors when a page rendered with at least
// one failed fragment and Options.AllowDegraded is false. The HTTP handler refuses
// to *cache* a degraded render precisely because it would pin one request's
// transient failure in front of every later one; a static build has no TTL to
// recover through, so writing it would pin that failure until the next deploy.
var ErrDegradedRender = errors.New("collage: refusing to write a degraded render")

// ErrEmptyRender is recorded in Report.Errors when a page rendered successfully but
// produced no markup at all — what an optional root fragment failing with no
// fallback produces, and what used to reach disk as a zero-byte index.html and an
// exit status of zero. Unlike a degraded render it is never written:
// Options.AllowDegraded is about serving a partial page, not an absent one.
var ErrEmptyRender = types.ErrEmptyRender

// ErrNotStatic is a SkipRecord's Err for a page or document whose render strategy
// is Dynamic(), which says it must not be stored — and a file is stored.
var ErrNotStatic = errors.New("collage: a Dynamic() route cannot be built statically")

// ErrUnresolvedToken is the SkipRecord's Err for a page rendered for a static build
// that contains a request-forgery token placeholder, and is recorded in
// Report.Errors when that page is the not-found page.
//
// A token is per reader, and the thing that replaces the placeholder with one is the
// running server. A built site has no server: the file would ship with the
// placeholder in it, and the form in it would be refused on submission with nothing
// to explain why — assuming there were anything to submit to, which there is not,
// because a form needs a server and a built site is files.
//
// So it is not written. A page is only known to carry a form once it has rendered,
// so a Static() page with one is skipped at that point and recorded in
// Report.Skipped with the reason — the same treatment a Dynamic() page gets before
// it renders, because it is the same situation: a page that belongs to the served
// site. It is a failure only for the not-found page, which a static host needs as a file.
var ErrUnresolvedToken = errors.New("collage: refusing to write a page whose forgery token was never resolved")

// unresolvedTokenReason is the SkipRecord.Reason of a page skipped for carrying a
// form.
//
// It prescribes no strategy. A page with a form may well be Static() on purpose —
// cached, and invalidated by tag from the action it posts to — and advice to make
// it Dynamic() would be advice to give that up. The page is fine; it is merely
// one that is served rather than exported.
const unresolvedTokenReason = "page carries {{csrfToken}}; a form needs a server to submit to, " +
	"so it is served rather than exported"

// Renderer is the narrow surface Builder needs from an application: what it
// contains — pages, documents, and mounted asset file systems — and a way to
// render a page or document by path outside the HTTP request path. It is
// declared here, rather than imported from internal/core, so this package can be
// tested against a fake and so the dependency between the two packages points from
// core to build.
//
// Pages, Documents, and Mounts are grouped on one interface, rather than Mounts
// living on Options instead, because all three answer the same question — what
// does this application contain — and a caller must not be able to hand New one
// application's Renderer alongside a different application's mounts: putting
// Mounts on Options would let exactly that happen, silently.
//
// Renderer is internal-only — it is not re-exported through pkg/collage, unlike
// PathProvider and DocumentPathProvider — so widening it to cover documents and
// mounts here breaks no external implementation of it; only *core.App itself has
// to satisfy the wider surface, and it already does.
//
// *core.App satisfies Renderer.
type Renderer interface {
	// Pages returns every registered page.
	Pages() []*types.Page
	// RenderPath renders the page registered at path for locale, overlaying params
	// onto whatever path parameters the router itself captures, and bypasses any
	// render cache.
	RenderPath(ctx context.Context, path, locale string, params map[string]string) (*render.Result, error)
	// Documents returns every registered document.
	Documents() []*types.Document
	// RenderDocumentPath renders the document registered at path for locale,
	// overlaying params onto whatever path parameters the router itself captures,
	// and bypasses any render cache. It is RenderPath's sibling for documents.
	RenderDocumentPath(ctx context.Context, path, locale string, params map[string]string) (*render.DocumentResult, error)
	// Mounts returns every mounted asset file system. A static build copies each
	// one whose BuildCopy is true into its output; see copyAssets.
	Mounts() []*asset.Mount
	// DefaultLocale is the locale served without a path prefix. Every other
	// locale's output is written under a directory named after it, mirroring the
	// URL the router answers; see localeOutputPath.
	DefaultLocale() string
	// CSRFMarker is the placeholder a rendered page carries where a
	// request-forgery token goes, or the empty string when the application has no
	// forgery protection. A static build refuses to write a page containing it:
	// see checkNoUnresolvedToken.
	CSRFMarker() string
	// RenderNotFound renders the registered not-found page for locale, or reports
	// that there is none with a nil result and a nil error. A not-found page has
	// no path, so RenderPath cannot reach it; a static host needs it as 404.html.
	RenderNotFound(ctx context.Context, locale string) (*render.Result, error)
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
	// Build additionally refuses to run at all — Clean or not — when OutDir
	// resolves to a filesystem root, and refuses to clean (though it still writes)
	// when OutDir resolves to a repository root; see ErrDangerousOutDir.
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
	// DocumentPathProvider supplies concrete paths for documents whose pattern
	// contains a "{param}" segment — PathProvider's sibling for documents. A
	// dynamic document with no DocumentPathProvider is recorded as skipped
	// rather than failing the build, wrapping the same ErrDynamicPathUnresolved
	// a dynamic page without a PathProvider records: the failure mode is
	// identical, only the kind of route differs.
	DocumentPathProvider DocumentPathProvider
	// AllowDegraded writes a page whose render had at least one failed fragment
	// instead of recording ErrDegradedRender against it. It is off by default:
	// a static file has no TTL, so a degraded page written to disk stays degraded
	// until the next build, which is the opposite of the HTTP handler's rule that
	// a degraded render is served but never cached.
	//
	// Turn it on when a partially rendered page is genuinely better than no page —
	// a site whose sidebar depends on an API that is down, say — and read
	// Report.Errors either way: enabling this does not make the failures invisible,
	// it only stops them from withholding the file. A render that produced no
	// markup at all is still refused, with ErrEmptyRender.
	AllowDegraded bool
}

// queryWarning is the warning for a route that reads query parameters, which a file
// cannot carry.
func queryWarning(name string, params []string) WarningRecord {
	return WarningRecord{
		Page: name,
		Reason: fmt.Sprintf("reads the query parameters %s, and a static file has no query string: only the version without them was written",
			strings.Join(params, ", ")),
	}
}

// WarningRecord describes a page or document the build wrote, but not all of.
type WarningRecord struct {
	// Page is the page's or document's Name, named Page for the reason
	// SkipRecord.Page is.
	Page string
	// Reason explains what the written file does not contain.
	Reason string
}

// SkipRecord describes one page or document, or one locale of one, that a static
// build could not produce, and why.
type SkipRecord struct {
	// Page is the skipped page's or document's Name. The field is not renamed for
	// documents: a page and a document are never skipped in the same pass over
	// the same registry, so one field unambiguously names whichever kind of route
	// this record describes.
	Page string
	// Locale is the specific locale that was skipped. It is empty when the whole
	// page or document was skipped regardless of locale (a non-cacheable render
	// strategy).
	Locale string
	// Reason explains why the page or document, or its locale, was skipped.
	Reason string
	// Err is the sentinel for the reason — ErrNotStatic, ErrDynamicPathUnresolved,
	// ErrUnresolvedToken, ErrDuplicateOutputPath — for a caller that acts on the
	// kind of skip rather than reading the sentence.
	Err error
}

// Report summarizes the outcome of a Build call.
type Report struct {
	// Written lists the absolute filesystem paths that were written, one per
	// rendered page or document, plus one per copied mounted asset file.
	Written []string
	// Skipped lists every page or document, or locale of one, the build could not
	// produce statically.
	Skipped []SkipRecord
	// Warnings lists every page or document that was written but is not the
	// whole of what the served one is — one whose content depends on a query
	// string, which a file has no way to carry.
	Warnings []WarningRecord
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

// Build renders every static-eligible page and document of the application to
// files under Options.OutDir, copies every mount the application reports through
// Renderer.Mounts whose BuildCopy is true into it, and returns a Report describing
// what happened.
//
// A page or document using a non-cacheable render strategy is skipped, recorded in
// the report, rather than built. A path pattern for a locale that contains a
// "{param}" segment is expanded through Options.PathProvider (pages) or
// Options.DocumentPathProvider (documents) when one is configured, or skipped with
// ErrDynamicPathUnresolved otherwise. A page that renders with a failed fragment is
// refused with ErrDegradedRender unless Options.AllowDegraded is set, and a page
// that renders no markup at all is always refused with ErrEmptyRender: a static
// file has no TTL to recover through, so writing either one pins it until the next
// build. A failure resolving paths, rendering, writing, or copying one page,
// document, or asset file does not stop the rest of the build: every such failure
// is recorded in Report.Errors, and the error Build returns is
// errors.Join(report.Errors...) — nil when there were none. A panic in a data
// handler is caught per page or document and recorded the same way; see the build
// worker in Build itself and its document-build sibling in buildDocuments.
//
// Build writes each rendered page to "<OutDir>/<path>/index.html", creating
// directories as needed; the root path "/" writes "<OutDir>/index.html". A
// document, unlike a page, writes to its own literal path — "/sitemap.xml" becomes
// "<OutDir>/sitemap.xml", not "<OutDir>/sitemap.xml/index.html" — because a
// crawler asking for it must not receive a directory; see documentTarget. A mounted
// asset file is written to "<OutDir>/<mount prefix><file name>", the same
// literal-path shape. Every resolved output path is verified to stay within OutDir
// both lexically and on disk — see ErrPathEscapesOutDir — since PathProvider,
// DocumentPathProvider, and a mount's fs.FS are all user code, and a path or file
// name built from unsanitised input, or a symlink planted anywhere under OutDir,
// must not be able to redirect a write outside the output directory.
//
// Options.Concurrency bounds how many pages, and separately how many documents,
// render and write at once, but Report.Written, Report.Skipped, and Report.Errors
// are always assembled in the same order Build enumerated pages, then documents,
// then mounted asset files, regardless of that concurrency: this package's output
// is deterministic by construction, not only at the default concurrency of 1.
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

	// Checked before anything renders, so a collision costs no work and leaves no
	// half-built directory. It has to be its own pass rather than a check inside
	// the write: the writes run concurrently, and "did anyone else already claim
	// this file" is exactly the question a concurrent writer cannot answer about
	// itself.
	if err := b.checkNoOutputCollisions(tasks); err != nil {
		enumerateErrs = append(enumerateErrs, err)
		tasks = nil
	}

	written := make([]string, len(tasks))
	taskErrs := make([]error, len(tasks))
	taskSkips := make([]*SkipRecord, len(tasks))

	sem := make(chan struct{}, b.opts.Concurrency)
	var wg sync.WaitGroup
	for i, task := range tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, task buildTask) {
			defer wg.Done()
			defer func() { <-sem }()
			// Registered last, so it runs first: a panic must land in taskErrs
			// before wg.Done releases Build to read that slot.
			//
			// Without it a panic in a data handler is process-fatal in the middle
			// of a build — and worst of all under Options.Clean, which has already
			// emptied OutDir by the time the first page renders. The render engine
			// recovers a panic inside Execute, but a PathProvider's data, a
			// fragment reached outside it, or a caller's own Renderer can still
			// panic here, and one page is not the whole site.
			defer func() {
				if recovered := recover(); recovered != nil {
					taskErrs[i] = fmt.Errorf("%w: page %q locale %q path %q: %v\n%s",
						ErrBuildPanic, task.page.Name, task.locale, task.path, recovered, debug.Stack())
				}
			}()
			target, err := b.renderAndWrite(ctx, outDirResolved, task)
			if errors.Is(err, ErrUnresolvedToken) {
				// Skipped rather than failed. Nothing is wrong with the page; it
				// has a form, and a form needs the server a built site lacks.
				taskSkips[i] = &SkipRecord{
					Page:   task.page.Name,
					Locale: task.locale,
					Reason: unresolvedTokenReason,
					Err:    ErrUnresolvedToken,
				}
				return
			}
			if err != nil {
				taskErrs[i] = err
				return
			}
			written[i] = target
		}(i, task)
	}
	wg.Wait()

	errs := append([]error(nil), enumerateErrs...)
	warned := make(map[*types.Page]bool)
	for i := range tasks {
		if taskErrs[i] != nil {
			errs = append(errs, taskErrs[i])
			continue
		}
		if taskSkips[i] != nil {
			report.Skipped = append(report.Skipped, *taskSkips[i])
			continue
		}
		report.Written = append(report.Written, written[i])

		// A page that declared which query parameters it reads renders
		// differently for each of them, and a file has no query string: a static
		// host answers /blogs?page=2 with the /blogs file, so pagination and
		// filters look like they work and do not. Written anyway — the page
		// without a query is a real page — but said.
		if page := tasks[i].page; len(page.CacheParams) > 0 && !warned[page] {
			warned[page] = true
			report.Warnings = append(report.Warnings, queryWarning(page.Name, page.CacheParams))
		}
	}

	docWritten, docSkipped, docWarnings, docErrs := b.buildDocuments(ctx, outDirResolved)
	report.Warnings = append(report.Warnings, docWarnings...)
	report.Skipped = append(report.Skipped, docSkipped...)
	report.Written = append(report.Written, docWritten...)
	errs = append(errs, docErrs...)

	// After the pages, because it renders one: whatever a plugin set up during the
	// page renders is set up by now, so the 404 page is built in the same state
	// every other page was.
	notFoundWritten, notFoundErrs := b.writeNotFoundPages(ctx, outDirResolved)
	report.Written = append(report.Written, notFoundWritten...)
	errs = append(errs, notFoundErrs...)

	assetWritten, assetErrs := b.copyAssets(outDirResolved)
	report.Written = append(report.Written, assetWritten...)
	errs = append(errs, assetErrs...)

	report.Errors = errs
	report.Duration = time.Since(start)

	if len(errs) == 0 {
		return report, nil
	}
	return report, errors.Join(errs...)
}

// checkNoOutputCollisions reports the first pair of tasks that would write to one
// file. Two pages whose patterns differ only in a trailing slash, or a PathProvider
// that returns the same path twice, both land here — and without the check the
// second write silently replaced the first, dropping a page from the build with
// nothing in the report to show for it.
//
// Locale is already folded in by localeOutputPath, so the same pattern in two
// locales is not a collision: that is the ordinary multi-locale case and each
// locale has its own directory.
func (b *Builder) checkNoOutputCollisions(tasks []buildTask) error {
	defaultLocale := b.app.DefaultLocale()
	claimed := make(map[string]buildTask, len(tasks))

	for _, task := range tasks {
		key := path.Clean(localeOutputPath(task.locale, defaultLocale, task.path))
		if previous, taken := claimed[key]; taken {
			return fmt.Errorf("%w: %q, claimed by page %q locale %q and page %q locale %q",
				ErrOutputPathCollision, key,
				previous.page.Name, previous.locale, task.page.Name, task.locale)
		}
		claimed[key] = task
	}
	return nil
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
		// A page with no path is not a URL, so there is nothing to export it as.
		// That is every error page — a not-found page, a server-error page —
		// which are reached by failing rather than by matching, and reporting
		// each of them as skipped every build says nothing anyone can act on.
		//
		// The not-found page is in the output all the same, as 404.html; see
		// writeNotFoundPages.
		if len(page.Paths) == 0 {
			continue
		}
		if !page.Strategy.Cacheable() {
			skipped = append(skipped, SkipRecord{
				Page:   page.Name,
				Reason: fmt.Sprintf("page uses the %s render strategy, which cannot be built statically", page.Strategy),
				Err:    ErrNotStatic,
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
					Err:    ErrDynamicPathUnresolved,
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

// localeOutputPath prefixes urlPath with the locale directory the router serves it
// under: nothing for the default locale, "/<locale>" for every other.
//
// This mirrors path-locale resolution rather than adding a convention of its own. A
// page registered at Paths{"tr": "/blog"} is reached at "/tr/blog" — the prefix is
// stripped before matching, so the author does not write it and writing it would
// produce "/tr/tr/blog". Without this, every locale of a page resolved to one file
// and the last render written won.
//
// A build is a set of files, so the default locale is the only one that can occupy
// the bare path. Which locale that is comes from the application rather than from
// Options: it is the same value the router resolves against, and two places to
// state it is one place for them to disagree.
func localeOutputPath(locale, defaultLocale, urlPath string) string {
	if locale == "" || locale == defaultLocale {
		return urlPath
	}
	if urlPath == "/" {
		return "/" + locale
	}
	return "/" + locale + urlPath
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
	target, err := resolveTarget(outDirResolved, localeOutputPath(task.locale, b.app.DefaultLocale(), task.path))
	if err != nil {
		return "", fmt.Errorf("collage: page %q locale %q: %w", task.page.Name, task.locale, err)
	}

	result, err := b.app.RenderPath(ctx, task.path, task.locale, task.params)
	if err != nil {
		return "", fmt.Errorf("collage: render page %q locale %q path %q: %w", task.page.Name, task.locale, task.path, err)
	}

	// A render can succeed and still be unfit to write. Both checks run before the
	// filesystem is touched at all, so a refused page leaves no file behind — not
	// even an empty one, which is what a caller running with Options.Clean would
	// otherwise be left serving.
	if result.Degraded() && !b.opts.AllowDegraded {
		return "", fmt.Errorf("%w: page %q locale %q path %q: %s", ErrDegradedRender, task.page.Name, task.locale, task.path, degradedSummary(result))
	}
	if len(result.HTML) == 0 {
		return "", fmt.Errorf("%w: page %q locale %q path %q", ErrEmptyRender, task.page.Name, task.locale, task.path)
	}
	if marker := b.app.CSRFMarker(); marker != "" && bytes.Contains(result.HTML, []byte(marker)) {
		return "", fmt.Errorf("%w: page %q locale %q path %q", ErrUnresolvedToken, task.page.Name, task.locale, task.path)
	}

	// resolveTarget's containment check is purely lexical: it proves the *string*
	// target stays inside outDirResolved and nothing about the filesystem. A
	// symlink planted anywhere under OutDir — a directory component or the leaf
	// file itself — can redirect the write below outside OutDir entirely. This
	// must run before os.MkdirAll, not after: MkdirAll itself follows symlinks
	// when it walks existing parent directories, so checking afterwards is too
	// late to catch what it would already have walked through.
	if err := verifyNoSymlinksBeneath(outDirResolved, target); err != nil {
		return "", fmt.Errorf("collage: page %q locale %q: %w", task.page.Name, task.locale, err)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("collage: create directory for %q: %w", target, err)
	}
	if err := os.WriteFile(target, result.HTML, 0o644); err != nil {
		return "", fmt.Errorf("collage: write %q: %w", target, err)
	}
	return target, nil
}

// degradedSummary names the fragments whose failure made result degraded, and the
// error each failed with, so Report.Errors says which component to go and look at
// rather than only that something went wrong. A result with no Metadata reports
// nothing to name, which Result.Degraded already treats as not degraded.
func degradedSummary(result *render.Result) string {
	if result == nil || result.Metadata == nil {
		return "no fragment metadata"
	}
	var failed []string
	for i := range result.Metadata.Fragments {
		fragment := result.Metadata.Fragments[i]
		if !fragment.Failed {
			continue
		}
		failed = append(failed, fmt.Sprintf("%s: %v", fragment.Name, fragment.Err))
	}
	return "failed fragments: " + strings.Join(failed, "; ")
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
//
// This check is purely lexical: it says nothing about whether a component that
// already exists on disk between outDirResolved and target is itself a symlink to
// somewhere else. See verifyNoSymlinksBeneath for that, which renderAndWrite calls
// separately before touching the filesystem.
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

// verifyNoSymlinksBeneath confirms that no path component from outDirResolved down
// to and including target — the very file about to be created — resolves, via a
// symlink, to somewhere outside outDirResolved. outDirResolved must itself be
// symlink-free (see prepareOutDir), so only the components rel adds beyond it need
// checking.
//
// A symlink component is not rejected outright merely for being a symlink:
// legitimate static-site layouts use them — a shared assets directory symlinked
// into the output, or artifacts carried between builds under Options.Clean: false
// — so a symlink is followed with filepath.EvalSymlinks and checked with the same
// filepath.Rel containment test resolveTarget uses. Only a symlink that resolves
// outside outDirResolved is rejected, with ErrPathEscapesOutDir; one that resolves
// back inside outDirResolved is allowed, and the write proceeds through it.
//
// A component that does not exist yet is not a symlink — there is nothing there to
// be one — so it is skipped rather than rejected: Build is expected to create fresh
// directories under OutDir on every run.
//
// This is a best-effort check, not a race-free guarantee: nothing stops another
// process from replacing a component with a symlink between this check and the
// os.MkdirAll/os.WriteFile calls that follow it in renderAndWrite. Closing that
// window portably (e.g. with O_NOFOLLOW, which is not available in a portable form
// from the standard library) is out of scope here. The threat model this closes is
// a symlink planted in advance of the build — by a malicious or buggy
// PathProvider, a misbehaving plugin, or a stale artifact left on disk from an
// earlier run — not a live local attacker racing the build process itself, which
// is a different and far less relevant threat for a builder the developer runs on
// their own machine.
func verifyNoSymlinksBeneath(outDirResolved, target string) error {
	rel, err := filepath.Rel(outDirResolved, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s", ErrPathEscapesOutDir, target)
	}
	if rel == "." {
		return nil
	}

	current := outDirResolved
	for _, segment := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("collage: stat %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}

		resolvedSymlink, err := filepath.EvalSymlinks(current)
		if err != nil {
			return fmt.Errorf("collage: resolve symlink %q: %w", current, err)
		}
		symlinkRel, err := filepath.Rel(outDirResolved, resolvedSymlink)
		if err != nil || symlinkRel == ".." || strings.HasPrefix(symlinkRel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: %s resolves to %s, outside the output directory", ErrPathEscapesOutDir, current, resolvedSymlink)
		}
	}
	return nil
}

// prepareOutDir resolves Options.OutDir to an absolute, symlink-resolved directory,
// refuses to proceed at all when that resolved directory is a filesystem root,
// refuses to additionally clean it (though a non-Clean build still writes into it)
// when it is a repository root, and returns the resolved path for the rest of
// Build to write under and check escapes against.
//
// The danger checks run against the resolved path, not the literal Options.OutDir
// string: OutDir can itself be a symlink — to "/", for instance — and
// isFilesystemRoot is a pure string comparison with no I/O of its own, so checking
// it against the unresolved string would fail open on exactly the case it exists
// to catch. Resolution happens before either danger check, not after.
func (b *Builder) prepareOutDir() (string, error) {
	absOutDir, err := filepath.Abs(b.opts.OutDir)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidOutDir, err)
	}
	cleanOutDir := filepath.Clean(absOutDir)

	resolved, err := resolveExistingPrefix(cleanOutDir)
	if err != nil {
		return "", fmt.Errorf("collage: resolve output directory %q: %w", cleanOutDir, err)
	}

	// Applies to every build, Clean or not: writing site output into "/" is
	// dangerous even when nothing is deleted first.
	if isFilesystemRoot(resolved) {
		return "", fmt.Errorf("%w: %s", ErrDangerousOutDir, resolved)
	}

	if b.opts.Clean {
		// Scoped to Clean, unlike the filesystem-root check above: building
		// (without deleting) into a repository root is a plausible setup — a
		// "docs/" or "dist/" directory inside the project — but deleting its
		// contents because Options.OutDir was left pointing at the wrong
		// directory is not something a blank or mistyped config field should be
		// able to trigger.
		if hasRepositoryMarker(resolved) {
			return "", fmt.Errorf("%w: %s", ErrDangerousOutDir, resolved)
		}
		if err := cleanDirContents(resolved); err != nil {
			return "", fmt.Errorf("collage: clean %q: %w", resolved, err)
		}
	}

	if err := os.MkdirAll(resolved, 0o755); err != nil {
		return "", fmt.Errorf("collage: create output directory %q: %w", resolved, err)
	}

	// Re-resolved after MkdirAll: resolveExistingPrefix above may have stopped
	// short of the full path if OutDir did not exist yet (filepath.EvalSymlinks
	// requires its argument to exist) and appended the missing suffix literally.
	// Now that MkdirAll has created it, resolving the complete path is possible,
	// and this is the value every per-file containment and symlink check is
	// measured against for the rest of Build.
	finalResolved, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", fmt.Errorf("collage: resolve output directory %q: %w", resolved, err)
	}
	return finalResolved, nil
}

// resolveExistingPrefix resolves symlinks in the longest existing ancestor of path
// and appends whatever suffix of path does not exist yet, unresolved: a path
// component that does not exist cannot be a symlink, and filepath.EvalSymlinks
// itself requires its argument to exist — it cannot be called on path directly
// when OutDir has not been created yet, which is the common case for a first
// build.
func resolveExistingPrefix(path string) (string, error) {
	existing := path
	var missing []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			// Nothing on the whole path exists. Break rather than loop forever;
			// the EvalSymlinks call below reports a clear error for this case.
			break
		}
		missing = append([]string{filepath.Base(existing)}, missing...)
		existing = parent
	}

	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if len(missing) == 0 {
		return resolved, nil
	}
	return filepath.Join(append([]string{resolved}, missing...)...), nil
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

// isFilesystemRoot reports whether dir — already absolute, filepath.Clean-ed, and
// symlink-resolved — is a filesystem root. filepath.Dir of a filesystem root
// returns the root itself; no other directory is its own parent, on Unix or
// Windows. Options.OutDir being empty is already rejected by New, for every
// build, not only a cleaning one, so it is not re-checked here.
func isFilesystemRoot(dir string) bool {
	return filepath.Dir(dir) == dir
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
