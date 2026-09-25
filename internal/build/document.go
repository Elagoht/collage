package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"

	"github.com/Elagoht/collage/internal/types"
)

// documentTask is one document, locale, and concrete path to render and write. It
// is buildTask's sibling for documents.
type documentTask struct {
	doc    *types.Document
	locale string
	path   string
	params map[string]string
}

// documentTarget returns the file a document's path is written to. Unlike a page,
// a document writes to its literal path: "/sitemap.xml" becomes
// "<OutDir>/sitemap.xml", not "<OutDir>/sitemap.xml/index.html", because a crawler
// asking for "/sitemap.xml" must not receive a directory.
//
// It reuses resolveTarget's containment check rather than re-deriving it, so the
// page and document write paths cannot drift apart on that logic: resolveTarget
// always shapes its result as "<...>/<rel>/index.html" regardless of what rel's own
// final segment already is, so once it has confirmed rel itself stays inside
// outDir, the literal document path is simply that trailing "index.html" component
// trimmed back off, not a second filepath.Rel/".." check written from scratch.
func documentTarget(outDir, urlPath string) (string, error) {
	pageShaped, err := resolveTarget(outDir, urlPath)
	if err != nil {
		return "", err
	}
	return filepath.Dir(pageShaped), nil
}

// enumerateDocuments walks every registered document and expands it into the
// concrete document tasks buildDocuments must render, alongside the documents or
// document locales that were skipped and the errors that came from resolving a
// dynamic document's paths. It never renders or writes anything itself, mirroring
// Builder.enumerate for documents.
func (b *Builder) enumerateDocuments(ctx context.Context) ([]documentTask, []SkipRecord, []error) {
	var tasks []documentTask
	var skipped []SkipRecord
	var errs []error

	allowedLocales := make(map[string]struct{}, len(b.opts.Locales))
	for _, locale := range b.opts.Locales {
		allowedLocales[locale] = struct{}{}
	}
	restrictLocales := len(allowedLocales) > 0

	for _, doc := range b.app.Documents() {
		if !doc.Strategy.Cacheable() {
			skipped = append(skipped, SkipRecord{
				Page:   doc.Name,
				Reason: fmt.Sprintf("document uses the %s render strategy, which cannot be built statically", doc.Strategy),
				Err:    ErrNotStatic,
			})
			continue
		}

		for _, locale := range doc.Locales() {
			// A document at the root is the site's, so in every build; Locales
			// restricts languages, and it is in none.
			if restrictLocales && locale != types.RootLocale {
				if _, ok := allowedLocales[locale]; !ok {
					continue
				}
			}

			pattern, ok := doc.PathFor(locale)
			if !ok {
				continue
			}

			if !isDynamicPattern(pattern) {
				tasks = append(tasks, documentTask{doc: doc, locale: locale, path: pattern})
				continue
			}

			instances, skip, err := expandPattern(ctx, "document", doc.Name, locale, pattern, doc.StaticParams)
			if skip != nil {
				skipped = append(skipped, *skip)
			}
			errs = append(errs, err...)
			for _, instance := range instances {
				tasks = append(tasks, documentTask{
					doc:    doc,
					locale: locale,
					path:   instance.path,
					params: instance.params,
				})
			}
		}
	}

	return tasks, skipped, errs
}

// documentURL is the URL a document task is served at, which is where it is
// written: its path under its locale's prefix, as a page's is. Writing a "tr"
// document to its bare path put it where the server answers the default locale,
// and left the URL that serves it a 404 on a static host.
func (b *Builder) documentURL(task documentTask) string {
	return b.pageOutputPath(task.locale, b.app.DefaultLocale(), task.path)
}

// dedupeDocumentTargets drops every task whose output file another task has
// already claimed, returning the surviving tasks and a SkipRecord for each drop.
//
// Each locale's document is written under that locale's URL — /tr/feed.xml for a
// "tr" document at "/feed.xml" — so one pattern in two locales is two files. Two
// tasks can still resolve to one file when StaticParams lists the same values
// twice. Left alone, and with Options.Concurrency above 1, both goroutines would
// call os.WriteFile on that path: one body would win nondeterministically, and
// nothing would report a problem.
//
// The first task to claim a path keeps it. Task order is deterministic —
// App.Documents is registration order and Document.Locales is sorted — so which
// locale wins is stable across builds rather than a race, and the losers are named
// in Report.Skipped instead of disappearing. A build that needs every locale's
// body on disk gives each locale its own pattern; that is a decision about URLs,
// which the builder is not entitled to make on the application's behalf, so it
// reports rather than invents a filename.
//
// Comparison is on the resolved target path, not on the URL pattern: two patterns
// can differ and still resolve to one file, and the file is what collides.
func (b *Builder) dedupeDocumentTargets(outDirResolved string, tasks []documentTask) ([]documentTask, []SkipRecord) {
	claimed := make(map[string]documentTask, len(tasks))
	kept := make([]documentTask, 0, len(tasks))
	var skipped []SkipRecord

	for _, task := range tasks {
		target, err := documentTarget(outDirResolved, b.documentURL(task))
		if err != nil {
			// Not this function's failure to report: renderAndWriteDocument
			// resolves the same target and turns the error into a build error
			// naming the document. Keeping the task preserves that, rather than
			// converting an error into a silent skip here.
			kept = append(kept, task)
			continue
		}

		if owner, taken := claimed[target]; taken {
			skipped = append(skipped, SkipRecord{
				Page:   task.doc.Name,
				Locale: task.locale,
				Reason: fmt.Sprintf("%v: path %q resolves to the same output file as locale %q's path %q",
					ErrDuplicateOutputPath, task.path, owner.locale, owner.path),
				Err: ErrDuplicateOutputPath,
			})
			continue
		}

		claimed[target] = task
		kept = append(kept, task)
	}

	return kept, skipped
}

// buildDocuments renders every static-eligible document to a file under
// outDirResolved and returns the absolute paths written, the documents or document
// locales that were skipped, and any errors encountered. It mirrors Build's own
// page loop — the same Options.Concurrency bound, the same per-task panic
// recovery, the same "one failure does not stop the rest" behaviour — with the one
// difference documentTarget documents: a document writes to its literal path, not
// "<path>/index.html".
func (b *Builder) buildDocuments(ctx context.Context, outDirResolved string) ([]string, []SkipRecord, []WarningRecord, []error) {
	tasks, skipped, errs := b.enumerateDocuments(ctx)

	// Before anything is rendered or written: two tasks that resolve to one file
	// must not both run, whatever Concurrency is set to.
	tasks, collisions := b.dedupeDocumentTargets(outDirResolved, tasks)
	skipped = append(skipped, collisions...)

	written := make([]string, len(tasks))
	taskErrs := make([]error, len(tasks))

	sem := make(chan struct{}, b.opts.Concurrency)
	var wg sync.WaitGroup
	for i, task := range tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, task documentTask) {
			defer wg.Done()
			defer func() { <-sem }()
			// See the identical comment in Build's own page worker: this must be
			// registered before renderAndWriteDocument runs, so a panic in a
			// document's handler lands in taskErrs rather than taking the whole
			// build process down.
			defer func() {
				if recovered := recover(); recovered != nil {
					taskErrs[i] = fmt.Errorf("%w: document %q locale %q path %q: %v\n%s",
						ErrBuildPanic, task.doc.Name, task.locale, task.path, recovered, debug.Stack())
				}
			}()
			target, err := b.renderAndWriteDocument(ctx, outDirResolved, task)
			if err != nil {
				taskErrs[i] = err
				return
			}
			written[i] = target
		}(i, task)
	}
	wg.Wait()

	var out []string
	var warnings []WarningRecord
	warned := make(map[*types.Document]bool)
	for i := range tasks {
		if taskErrs[i] != nil {
			errs = append(errs, taskErrs[i])
			continue
		}
		out = append(out, written[i])
		// The same warning a page gets: a document that reads the query — a
		// paginated feed — has no query string as a file.
		if doc := tasks[i].doc; len(doc.CacheParams) > 0 && !warned[doc] {
			warned[doc] = true
			warnings = append(warnings, queryWarning(doc.Name, doc.CacheParams))
		}
	}
	return out, skipped, warnings, errs
}

// renderAndWriteDocument renders one document task through the application and
// writes the result under outDirResolved, which must already be an absolute,
// symlink-resolved directory (see prepareOutDir). It returns the absolute path
// written. It mirrors renderAndWrite, using documentTarget in place of
// resolveTarget for the one behavioural difference between a page and a document.
func (b *Builder) renderAndWriteDocument(ctx context.Context, outDirResolved string, task documentTask) (string, error) {
	target, err := documentTarget(outDirResolved, b.documentURL(task))
	if err != nil {
		return "", fmt.Errorf("collage: document %q locale %q: %w", task.doc.Name, task.locale, err)
	}

	result, err := b.app.RenderDocumentPath(ctx, task.path, task.locale, task.params)
	if err != nil {
		return "", fmt.Errorf("collage: render document %q locale %q path %q: %w", task.doc.Name, task.locale, task.path, err)
	}

	// A render can succeed and still be unfit to write. internal/httpx refuses to
	// *serve* an empty document body — types.ErrEmptyDocumentBody, a 500 —
	// because a document has no equivalent of a page's optional root fragment,
	// whose empty render is deliberate; an empty body is indistinguishable from a
	// handler that forgot to populate it. RenderDocumentPath bypasses httpx
	// entirely, so that check has to be repeated here, reusing the same sentinel
	// from internal/types (which both this package and internal/httpx import
	// independently, rather than this package importing internal/httpx for it)
	// instead of inventing a second one for what is the same failure mode: a
	// static build must not silently write the zero-byte file the live server
	// would have refused to serve. This runs before anything touches the
	// filesystem, the same way renderAndWrite's own degraded/empty checks do for
	// a page.
	if len(result.Body) == 0 {
		return "", fmt.Errorf("%w: document %q locale %q path %q", types.ErrEmptyDocumentBody, task.doc.Name, task.locale, task.path)
	}

	// Same reasoning as renderAndWrite: this filesystem-aware check must run
	// before os.MkdirAll, not after, because MkdirAll itself follows symlinks
	// when it walks existing parent directories.
	if err := verifyNoSymlinksBeneath(outDirResolved, target); err != nil {
		return "", fmt.Errorf("collage: document %q locale %q: %w", task.doc.Name, task.locale, err)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("collage: create directory for %q: %w", target, err)
	}
	if err := os.WriteFile(target, result.Body, 0o644); err != nil {
		return "", fmt.Errorf("collage: write %q: %w", target, err)
	}
	return target, nil
}
