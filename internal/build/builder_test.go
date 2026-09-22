package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/asset"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/types"
)

// fakeRenderer is a Renderer built from a fixed set of pages and a render hook,
// standing in for *core.App so this package is testable without wiring up a real
// template engine, router, and render pipeline.
type fakeRenderer struct {
	pages []*types.Page

	mu    sync.Mutex
	calls []renderCall
	// fail maps a call key (see renderKey) to the error RenderPath returns for it.
	fail map[string]error
	// delays maps a call key (see renderKey) to an artificial delay RenderPath
	// sleeps for before returning, used to force goroutines to finish out of
	// dispatch order under a concurrent Build.
	delays map[string]time.Duration
	// degrade maps a call key (see renderKey) to the name of a fragment that
	// failed during that render. RenderPath then returns a successful Result
	// carrying failed fragment metadata — a *degraded* render, which is a success
	// with a nil error and is the shape this fake could not previously produce.
	degrade map[string]string
	// empty holds the call keys (see renderKey) whose render succeeds and produces
	// no markup at all, the way an optional root fragment failing with no fallback
	// does.
	empty map[string]bool
	// panics holds the call keys (see renderKey) whose render panics, standing in
	// for a data handler or a caller-supplied Renderer that panics mid-build.
	panics map[string]bool

	documents []*types.Document
	docCalls  []renderCall
	// docFail maps a call key (see renderKey) to the error RenderDocumentPath
	// returns for it.
	docFail map[string]error
	// docBodies maps a call key (see renderKey) to the exact bytes
	// RenderDocumentPath returns for it. A key with no entry falls back to a body
	// derived from locale and path, mirroring RenderPath's own fallback content.
	docBodies map[string][]byte
	// docPanics holds the call keys (see renderKey) whose document render panics.
	docPanics map[string]bool

	mounts []*asset.Mount
}

type renderCall struct {
	path   string
	locale string
	params map[string]string
}

func renderKey(path, locale string) string {
	return locale + "\x00" + path
}

func (f *fakeRenderer) Pages() []*types.Page {
	return f.pages
}

func (f *fakeRenderer) RenderPath(_ context.Context, path, locale string, params map[string]string) (*render.Result, error) {
	key := renderKey(path, locale)

	f.mu.Lock()
	f.calls = append(f.calls, renderCall{path: path, locale: locale, params: params})
	failErr, shouldFail := f.fail[key]
	delay := f.delays[key]
	degradedFragment, degraded := f.degrade[key]
	empty := f.empty[key]
	shouldPanic := f.panics[key]
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if shouldPanic {
		panic("fakeRenderer: deliberate panic for " + key)
	}
	if shouldFail {
		return nil, failErr
	}

	result := &render.Result{
		HTML: fmt.Appendf(nil, "<html>%s|%s</html>", locale, path),
	}
	if empty {
		// Not an error: Render documents a nil HTML with a nil error as what an
		// optional root fragment failing with no fallback produces.
		result.HTML = nil
	}
	if degraded {
		result.Metadata = &render.Metadata{
			Page:   page(f, path, locale),
			Locale: locale,
			Fragments: []render.FragmentMetadata{
				{Name: degradedFragment, Failed: true, Err: errFragmentFailed},
			},
		}
	}
	return result, nil
}

// errFragmentFailed is the error the fake reports on a degraded render's failed
// fragment, so an assertion can check the summary names the real cause.
var errFragmentFailed = errors.New("sidebar backend unreachable")

// page returns the name of the page f would have rendered at path for locale, or
// the path itself when no registered page claims it. It exists only so a degraded
// Result carries plausible Metadata.
func page(f *fakeRenderer, path, locale string) string {
	for _, p := range f.pages {
		if pattern, ok := p.PathFor(locale); ok && pattern == path {
			return p.Name
		}
	}
	return path
}

func (f *fakeRenderer) Documents() []*types.Document {
	return f.documents
}

func (f *fakeRenderer) RenderDocumentPath(_ context.Context, path, locale string, params map[string]string) (*render.DocumentResult, error) {
	key := renderKey(path, locale)

	f.mu.Lock()
	f.docCalls = append(f.docCalls, renderCall{path: path, locale: locale, params: params})
	failErr, shouldFail := f.docFail[key]
	body, hasBody := f.docBodies[key]
	shouldPanic := f.docPanics[key]
	f.mu.Unlock()

	if shouldPanic {
		panic("fakeRenderer: deliberate document panic for " + key)
	}
	if shouldFail {
		return nil, failErr
	}
	if !hasBody {
		body = fmt.Appendf(nil, "%s|%s", locale, path)
	}
	return &render.DocumentResult{
		Body:        body,
		ContentType: "application/octet-stream",
	}, nil
}

func (f *fakeRenderer) Mounts() []*asset.Mount {
	return f.mounts
}

var _ Renderer = (*fakeRenderer)(nil)

// fakePathProvider is a PathProvider backed by a fixed table, keyed by page name and
// locale, or a fixed error.
type fakePathProvider struct {
	instances map[string][]PathInstance
	err       error
}

func (p *fakePathProvider) Paths(_ context.Context, page *types.Page, locale string) ([]PathInstance, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.instances[page.Name+"|"+locale], nil
}

var _ PathProvider = (*fakePathProvider)(nil)

// newTestPage builds a minimally valid *types.Page for these tests: Builder never
// calls Page.Validate, so only the fields Builder itself reads need to be set.
func newTestPage(name string, strategy types.RenderStrategy, paths map[string]string) *types.Page {
	return &types.Page{
		Name:            name,
		Strategy:        strategy,
		Paths:           paths,
		ContentFragment: &types.Fragment{Name: "content", TemplatePath: "content.html"},
	}
}

// resolvedTempDir returns a fresh temp directory, already filepath.EvalSymlinks-
// resolved, so tests that compare Report.Written by exact string equality are not
// tripped up by a symlinked temp root (macOS's /tmp -> /private/tmp, and the same
// shape under /var/folders) the way Build itself is careful to avoid via the same
// resolution in prepareOutDir.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", dir, err)
	}
	return resolved
}

// requireSymlinkSupport skips t when the current environment cannot create
// symlinks (e.g. Windows without the privilege or developer mode enabled), so the
// symlink-escape tests degrade to a skip rather than a failure where they cannot
// run at all.
func requireSymlinkSupport(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("write probe target: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not supported in this environment: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestNew_NilRenderer(t *testing.T) {
	_, err := New(nil, Options{OutDir: t.TempDir()})
	if !errors.Is(err, ErrNilRenderer) {
		t.Fatalf("err = %v, want ErrNilRenderer", err)
	}
}

func TestNew_EmptyOutDir(t *testing.T) {
	_, err := New(&fakeRenderer{}, Options{})
	if !errors.Is(err, ErrInvalidOutDir) {
		t.Fatalf("err = %v, want ErrInvalidOutDir", err)
	}
}

func TestNew_DefaultsConcurrency(t *testing.T) {
	b, err := New(&fakeRenderer{}, Options{OutDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if b.opts.Concurrency != 1 {
		t.Fatalf("Concurrency = %d, want 1", b.opts.Concurrency)
	}
}

func TestBuild_StaticPage(t *testing.T) {
	out := resolvedTempDir(t)
	page := newTestPage("about", types.StrategyStatic, map[string]string{"en": "/about"})
	app := &fakeRenderer{pages: []*types.Page{page}}

	b, err := New(app, Options{OutDir: out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := filepath.Join(out, "about", "index.html")
	got := readFile(t, want)
	if got != "<html>en|/about</html>" {
		t.Fatalf("content = %q", got)
	}
	if len(report.Written) != 1 || report.Written[0] != want {
		t.Fatalf("Written = %v, want [%s]", report.Written, want)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("Errors = %v", report.Errors)
	}
}

func TestBuild_RootPath(t *testing.T) {
	out := resolvedTempDir(t)
	page := newTestPage("home", types.StrategyStatic, map[string]string{"en": "/"})
	app := &fakeRenderer{pages: []*types.Page{page}}

	b, err := New(app, Options{OutDir: out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := b.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := filepath.Join(out, "index.html")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("stat %s: %v", want, err)
	}
}

func TestBuild_MultipleLocales(t *testing.T) {
	out := resolvedTempDir(t)
	page := newTestPage("blog", types.StrategyStatic, map[string]string{
		"en": "/blog",
		"tr": "/tr/blog",
	})
	app := &fakeRenderer{pages: []*types.Page{page}}

	b, err := New(app, Options{OutDir: out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	en := filepath.Join(out, "blog", "index.html")
	tr := filepath.Join(out, "tr", "blog", "index.html")
	if _, err := os.Stat(en); err != nil {
		t.Fatalf("stat %s: %v", en, err)
	}
	if _, err := os.Stat(tr); err != nil {
		t.Fatalf("stat %s: %v", tr, err)
	}
	if len(report.Written) != 2 {
		t.Fatalf("Written = %v, want 2 entries", report.Written)
	}
}

func TestBuild_LocaleFilter(t *testing.T) {
	out := resolvedTempDir(t)
	page := newTestPage("blog", types.StrategyStatic, map[string]string{
		"en": "/blog",
		"tr": "/tr/blog",
	})
	app := &fakeRenderer{pages: []*types.Page{page}}

	b, err := New(app, Options{OutDir: out, Locales: []string{"tr"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(report.Written) != 1 {
		t.Fatalf("Written = %v, want 1 entry", report.Written)
	}
	if _, err := os.Stat(filepath.Join(out, "blog", "index.html")); err == nil {
		t.Fatalf("en/blog was written despite Locales filter")
	}
}

func TestBuild_DynamicPage_WithPathProvider(t *testing.T) {
	out := resolvedTempDir(t)
	page := newTestPage("post", types.StrategyStatic, map[string]string{"en": "/blog/{slug}"})
	app := &fakeRenderer{pages: []*types.Page{page}}
	provider := &fakePathProvider{instances: map[string][]PathInstance{
		"post|en": {
			{Path: "/blog/hello", Params: map[string]string{"slug": "hello"}},
			{Path: "/blog/world", Params: map[string]string{"slug": "world"}},
		},
	}}

	b, err := New(app, Options{OutDir: out, PathProvider: provider})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	hello := filepath.Join(out, "blog", "hello", "index.html")
	world := filepath.Join(out, "blog", "world", "index.html")
	if _, err := os.Stat(hello); err != nil {
		t.Fatalf("stat %s: %v", hello, err)
	}
	if _, err := os.Stat(world); err != nil {
		t.Fatalf("stat %s: %v", world, err)
	}
	if len(report.Written) != 2 {
		t.Fatalf("Written = %v, want 2 entries", report.Written)
	}

	// The params the provider supplied must have reached RenderPath.
	app.mu.Lock()
	defer app.mu.Unlock()
	found := false
	for _, call := range app.calls {
		if call.path == "/blog/hello" && call.params["slug"] == "hello" {
			found = true
		}
	}
	if !found {
		t.Fatalf("RenderPath was not called with the provider's params: %+v", app.calls)
	}
}

func TestBuild_DynamicPage_NoProvider_Skipped(t *testing.T) {
	out := resolvedTempDir(t)
	page := newTestPage("post", types.StrategyStatic, map[string]string{"en": "/blog/{slug}"})
	app := &fakeRenderer{pages: []*types.Page{page}}

	b, err := New(app, Options{OutDir: out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build returned an error for a skip, want nil: %v", err)
	}

	if len(report.Skipped) != 1 {
		t.Fatalf("Skipped = %v, want 1 entry", report.Skipped)
	}
	got := report.Skipped[0]
	if got.Page != "post" || got.Locale != "en" || got.Reason != ErrDynamicPathUnresolved.Error() {
		t.Fatalf("Skipped[0] = %+v", got)
	}
	if len(report.Written) != 0 {
		t.Fatalf("Written = %v, want none", report.Written)
	}
}

func TestBuild_DynamicStrategySkipped(t *testing.T) {
	out := resolvedTempDir(t)
	page := newTestPage("live", types.StrategyDynamic, map[string]string{"en": "/live"})
	app := &fakeRenderer{pages: []*types.Page{page}}

	b, err := New(app, Options{OutDir: out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(report.Skipped) != 1 || report.Skipped[0].Page != "live" || report.Skipped[0].Locale != "" {
		t.Fatalf("Skipped = %+v", report.Skipped)
	}
	if len(report.Written) != 0 {
		t.Fatalf("Written = %v, want none", report.Written)
	}
}

// TestBuild_PathProvider_Escape is the required path-escape test: a PathProvider is
// user code, and a Path that resolves outside OutDir after filepath.Clean must be
// rejected, and nothing must be written outside OutDir.
func TestBuild_PathProvider_Escape(t *testing.T) {
	out := resolvedTempDir(t)
	page := newTestPage("post", types.StrategyStatic, map[string]string{"en": "/blog/{slug}"})
	app := &fakeRenderer{pages: []*types.Page{page}}
	provider := &fakePathProvider{instances: map[string][]PathInstance{
		"post|en": {
			{Path: "/../../../etc/cron.d/evil", Params: map[string]string{"slug": "../../../etc/cron.d/evil"}},
		},
	}}

	b, err := New(app, Options{OutDir: out, PathProvider: provider})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, buildErr := b.Build(context.Background())
	if buildErr == nil {
		t.Fatal("Build succeeded for an escaping path, want an error")
	}
	if !errors.Is(buildErr, ErrPathEscapesOutDir) {
		t.Fatalf("err = %v, want ErrPathEscapesOutDir", buildErr)
	}
	if len(report.Written) != 0 {
		t.Fatalf("Written = %v, want none", report.Written)
	}

	// Nothing was written anywhere outside out: out's parent must contain only
	// what it already contained (out itself), and the escape target must not
	// exist.
	if _, err := os.Stat(filepath.Join(out, "..", "..", "..", "etc", "cron.d", "evil")); err == nil {
		t.Fatal("escape target was written to disk")
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("OutDir has unexpected entries: %v", entries)
	}
}

// TestBuild_PathProvider_SymlinkEscape is the required test reproducing the C1
// review finding: resolveTarget's containment check is purely lexical
// (filepath.Clean + filepath.Rel), which proves nothing about the filesystem. A
// symlink planted under OutDir — here, OutDir/escaped pointing at a sibling
// directory entirely outside OutDir — must still be rejected before any bytes are
// written through it, by the separate, filesystem-aware verifyNoSymlinksBeneath
// check. This is the "resolves outside OutDir, must be rejected" half of that
// check's behaviour; TestBuild_PathProvider_SymlinkWithinOutDir_Allowed below is
// the other half ("resolves inside OutDir, must be allowed").
func TestBuild_PathProvider_SymlinkEscape(t *testing.T) {
	requireSymlinkSupport(t)

	out := resolvedTempDir(t)
	external := resolvedTempDir(t)

	link := filepath.Join(out, "escaped")
	if err := os.Symlink(external, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	page := newTestPage("post", types.StrategyStatic, map[string]string{"en": "/escaped/{slug}"})
	app := &fakeRenderer{pages: []*types.Page{page}}
	provider := &fakePathProvider{instances: map[string][]PathInstance{
		"post|en": {
			{Path: "/escaped/pwned", Params: map[string]string{"slug": "pwned"}},
		},
	}}

	b, err := New(app, Options{OutDir: out, PathProvider: provider})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, buildErr := b.Build(context.Background())
	if buildErr == nil {
		t.Fatal("Build succeeded for a write redirected through a symlink, want an error")
	}
	if !errors.Is(buildErr, ErrPathEscapesOutDir) {
		t.Fatalf("err = %v, want ErrPathEscapesOutDir", buildErr)
	}
	if len(report.Written) != 0 {
		t.Fatalf("Written = %v, want none", report.Written)
	}

	// The critical assertion: nothing was written through the symlink into
	// external, the directory genuinely outside OutDir.
	if _, err := os.Stat(filepath.Join(external, "pwned")); err == nil {
		t.Fatal("a file was written outside OutDir through the symlink")
	}
	entries, err := os.ReadDir(external)
	if err != nil {
		t.Fatalf("ReadDir(external): %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("external has unexpected entries: %v", entries)
	}
}

// TestBuild_PathProvider_SymlinkWithinOutDir_Allowed is the fix-round-2 test: a
// symlink is not rejected merely for being a symlink — only one that resolves
// outside OutDir is. Legitimate layouts rely on this (a shared assets directory
// symlinked into the output, or artifacts carried between builds under
// Options.Clean: false). Here OutDir/shortcut is a symlink to OutDir/realdir, both
// inside OutDir, and the write through it must succeed.
func TestBuild_PathProvider_SymlinkWithinOutDir_Allowed(t *testing.T) {
	requireSymlinkSupport(t)

	out := resolvedTempDir(t)
	realDir := filepath.Join(out, "realdir")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(realDir): %v", err)
	}
	shortcut := filepath.Join(out, "shortcut")
	if err := os.Symlink(realDir, shortcut); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	page := newTestPage("post", types.StrategyStatic, map[string]string{"en": "/shortcut/{slug}"})
	app := &fakeRenderer{pages: []*types.Page{page}}
	provider := &fakePathProvider{instances: map[string][]PathInstance{
		"post|en": {
			{Path: "/shortcut/hello", Params: map[string]string{"slug": "hello"}},
		},
	}}

	b, err := New(app, Options{OutDir: out, PathProvider: provider})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v (a symlink resolving inside OutDir must be allowed, not rejected)", err)
	}
	if len(report.Written) != 1 {
		t.Fatalf("Written = %v, want 1 entry", report.Written)
	}

	// Readable through the lexical path Build recorded in Report.Written...
	content := readFile(t, report.Written[0])
	if content != "<html>en|/shortcut/hello</html>" {
		t.Fatalf("content = %q", content)
	}
	// ...and physically present under the symlink's real target, since writing
	// through a symlink and writing to its target are the same file on disk.
	viaRealTarget := filepath.Join(realDir, "hello", "index.html")
	if got := readFile(t, viaRealTarget); got != content {
		t.Fatalf("content via the symlink's real target = %q, want %q", got, content)
	}
}

// TestBuild_Clean_RefusesFilesystemRoot verifies that Clean never even attempts to
// touch "/": the refusal is a pure string check on the resolved path, so it is safe
// to assert here without actually risking anything on disk.
func TestBuild_Clean_RefusesFilesystemRoot(t *testing.T) {
	app := &fakeRenderer{}
	b, err := New(app, Options{OutDir: "/", Clean: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = b.Build(context.Background())
	if !errors.Is(err, ErrDangerousOutDir) {
		t.Fatalf("err = %v, want ErrDangerousOutDir", err)
	}
}

// TestBuild_RefusesFilesystemRoot_WithoutClean verifies the filesystem-root refusal
// applies to every build, not only a cleaning one: writing site output directly
// into "/" is dangerous even when nothing is deleted first.
func TestBuild_RefusesFilesystemRoot_WithoutClean(t *testing.T) {
	app := &fakeRenderer{}
	b, err := New(app, Options{OutDir: "/", Clean: false})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = b.Build(context.Background())
	if !errors.Is(err, ErrDangerousOutDir) {
		t.Fatalf("err = %v, want ErrDangerousOutDir", err)
	}
}

// TestBuild_Clean_RefusesSymlinkedFilesystemRoot is the required test reproducing
// the C2 review finding: the filesystem-root check (filepath.Dir(dir) == dir) is a
// pure string comparison with no I/O, so run against the unresolved OutDir string
// it fails open on an OutDir that is itself a symlink to "/". prepareOutDir must
// resolve symlinks before running that check, not after.
func TestBuild_Clean_RefusesSymlinkedFilesystemRoot(t *testing.T) {
	requireSymlinkSupport(t)

	parent := t.TempDir()
	link := filepath.Join(parent, "root-link")
	if err := os.Symlink(string(filepath.Separator), link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	app := &fakeRenderer{}
	b, err := New(app, Options{OutDir: link, Clean: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, buildErr := b.Build(context.Background())
	if !errors.Is(buildErr, ErrDangerousOutDir) {
		t.Fatalf("err = %v, want ErrDangerousOutDir", buildErr)
	}
}

// TestBuild_Clean_RefusesRepositoryRoot verifies that a directory that looks like a
// repository root (it directly contains a go.mod) is refused, and — critically —
// that nothing inside it was deleted by the attempt.
func TestBuild_Clean_RefusesRepositoryRoot(t *testing.T) {
	repo := t.TempDir()
	marker := filepath.Join(repo, "go.mod")
	if err := os.WriteFile(marker, []byte("module example\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	sentinel := filepath.Join(repo, "keepme.txt")
	if err := os.WriteFile(sentinel, []byte("do not delete"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	app := &fakeRenderer{}
	b, err := New(app, Options{OutDir: repo, Clean: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, buildErr := b.Build(context.Background())
	if !errors.Is(buildErr, ErrDangerousOutDir) {
		t.Fatalf("err = %v, want ErrDangerousOutDir", buildErr)
	}

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("sentinel file was deleted: %v", err)
	}
}

// TestBuild_Clean_RemovesExistingContents verifies the happy path of Clean: a
// legitimate, non-dangerous OutDir has its stale contents removed before the new
// build writes into it.
func TestBuild_Clean_RemovesExistingContents(t *testing.T) {
	out := resolvedTempDir(t)
	stale := filepath.Join(out, "stale.html")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	page := newTestPage("about", types.StrategyStatic, map[string]string{"en": "/about"})
	app := &fakeRenderer{pages: []*types.Page{page}}
	b, err := New(app, Options{OutDir: out, Clean: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := b.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale file still exists after Clean: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "about", "index.html")); err != nil {
		t.Fatalf("stat about/index.html: %v", err)
	}
}

// TestBuild_FailingPageDoesNotAbort is the required test that one bad page does not
// hide the rest: a page whose render fails is recorded as an error, and the build
// still writes every other page.
func TestBuild_FailingPageDoesNotAbort(t *testing.T) {
	out := resolvedTempDir(t)
	broken := newTestPage("broken", types.StrategyStatic, map[string]string{"en": "/broken"})
	ok := newTestPage("ok", types.StrategyStatic, map[string]string{"en": "/ok"})
	renderErr := errors.New("boom")
	app := &fakeRenderer{
		pages: []*types.Page{broken, ok},
		fail:  map[string]error{renderKey("/broken", "en"): renderErr},
	}

	b, err := New(app, Options{OutDir: out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, buildErr := b.Build(context.Background())
	if buildErr == nil {
		t.Fatal("Build succeeded despite a failing page, want an error")
	}
	if !errors.Is(buildErr, renderErr) {
		t.Fatalf("err = %v, want it to wrap %v", buildErr, renderErr)
	}

	if len(report.Errors) != 1 {
		t.Fatalf("Errors = %v, want 1 entry", report.Errors)
	}
	if len(report.Written) != 1 {
		t.Fatalf("Written = %v, want 1 entry (the page that succeeded)", report.Written)
	}
	want := filepath.Join(out, "ok", "index.html")
	if report.Written[0] != want {
		t.Fatalf("Written[0] = %s, want %s", report.Written[0], want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("stat %s: %v", want, err)
	}
	if _, err := os.Stat(filepath.Join(out, "broken", "index.html")); err == nil {
		t.Fatal("the failing page's file should not exist")
	}
}

// TestBuild_ReportContents exercises every field of Report together: a written
// page, a skipped page, a failing page, and a non-zero Duration.
func TestBuild_ReportContents(t *testing.T) {
	out := resolvedTempDir(t)
	written := newTestPage("written", types.StrategyStatic, map[string]string{"en": "/written"})
	skipped := newTestPage("skipped", types.StrategyDynamic, map[string]string{"en": "/skipped"})
	failing := newTestPage("failing", types.StrategyStatic, map[string]string{"en": "/failing"})
	renderErr := errors.New("render exploded")
	app := &fakeRenderer{
		pages: []*types.Page{written, skipped, failing},
		fail:  map[string]error{renderKey("/failing", "en"): renderErr},
	}

	b, err := New(app, Options{OutDir: out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, buildErr := b.Build(context.Background())
	if buildErr == nil {
		t.Fatal("Build succeeded despite a failing page, want an error")
	}

	if len(report.Written) != 1 || report.Written[0] != filepath.Join(out, "written", "index.html") {
		t.Fatalf("Written = %v", report.Written)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Page != "skipped" {
		t.Fatalf("Skipped = %+v", report.Skipped)
	}
	if len(report.Errors) != 1 || !errors.Is(report.Errors[0], renderErr) {
		t.Fatalf("Errors = %v", report.Errors)
	}
	if report.Duration <= 0 {
		t.Fatalf("Duration = %v, want > 0", report.Duration)
	}
	if !errors.Is(buildErr, renderErr) {
		t.Fatalf("build err = %v, want it to wrap %v", buildErr, renderErr)
	}
}

// TestBuild_IncrementalStrategyIsBuilt verifies StrategyIncremental, like
// StrategyStatic, is built — only StrategyDynamic is unbuildable by definition.
func TestBuild_IncrementalStrategyIsBuilt(t *testing.T) {
	out := resolvedTempDir(t)
	page := newTestPage("incr", types.StrategyIncremental, map[string]string{"en": "/incr"})
	app := &fakeRenderer{pages: []*types.Page{page}}

	b, err := New(app, Options{OutDir: out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.Written) != 1 {
		t.Fatalf("Written = %v, want 1 entry", report.Written)
	}
}

// TestResolveTarget_RejectsSiblingLookalike pins the exact trap the task brief
// warns about: a string-prefix containment check (strings.HasPrefix(target, root))
// would wrongly accept "/outsibling" as being inside "/out", because "/outsibling"
// starts with the literal characters "/out". The path-segment-aware check
// (filepath.Rel plus a ".." prefix check) resolveTarget actually uses must reject
// it. The outDirResolved here ends in a directory literally named "out" so the
// escape lands on a directory whose name has "out" as a prefix, not merely a
// coincidental temp-dir name.
func TestResolveTarget_RejectsSiblingLookalike(t *testing.T) {
	parent := t.TempDir()
	outDirResolved := filepath.Join(parent, "out")
	if err := os.MkdirAll(outDirResolved, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// A naive strings.HasPrefix check on this exact pair would wrongly say
	// "contained": confirm that premise so the test documents the trap it guards
	// against, then confirm resolveTarget is not fooled by it.
	sibling := filepath.Join(parent, "outsibling", "index.html")
	if !strings.HasPrefix(sibling, outDirResolved) {
		t.Fatalf("test setup invariant broken: %q is not a HasPrefix match for %q", sibling, outDirResolved)
	}

	_, err := resolveTarget(outDirResolved, "/../outsibling")
	if !errors.Is(err, ErrPathEscapesOutDir) {
		t.Fatalf("err = %v, want ErrPathEscapesOutDir", err)
	}

	// A legitimate path under OutDir must still resolve normally.
	target, err := resolveTarget(outDirResolved, "/about")
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if target != filepath.Join(outDirResolved, "about", "index.html") {
		t.Fatalf("target = %s", target)
	}
}

// TestBuild_ConcurrencyPreservesDeterministicOrder is the required coverage for
// Options.Concurrency above 1: a build at Concurrency: 8, over enough pages to
// interleave, must still assemble Report.Written in the same order a sequential
// (Concurrency: 1) build over the same pages would, and must write byte-identical
// output. delays are assigned in reverse of enumeration order — the first-
// enumerated page sleeps longest, the last-enumerated page sleeps least — so with
// real concurrency the goroutines finish in close to the opposite order from how
// they were dispatched, which is exactly the case that would expose a build that
// merges results in completion order instead of enumeration order.
func TestBuild_ConcurrencyPreservesDeterministicOrder(t *testing.T) {
	const n = 20
	pages := make([]*types.Page, n)
	delays := make(map[string]time.Duration, n)
	for i := range n {
		name := fmt.Sprintf("page%02d", i)
		path := fmt.Sprintf("/p%02d", i)
		pages[i] = newTestPage(name, types.StrategyStatic, map[string]string{"en": path})
		delays[renderKey(path, "en")] = time.Duration(n-i) * 2 * time.Millisecond
	}

	seqOut := resolvedTempDir(t)
	seqApp := &fakeRenderer{pages: pages, delays: delays}
	seqBuilder, err := New(seqApp, Options{OutDir: seqOut, Concurrency: 1})
	if err != nil {
		t.Fatalf("New (sequential): %v", err)
	}
	seqReport, err := seqBuilder.Build(context.Background())
	if err != nil {
		t.Fatalf("Build (sequential): %v", err)
	}

	concOut := resolvedTempDir(t)
	concApp := &fakeRenderer{pages: pages, delays: delays}
	concBuilder, err := New(concApp, Options{OutDir: concOut, Concurrency: 8})
	if err != nil {
		t.Fatalf("New (concurrent): %v", err)
	}
	concReport, err := concBuilder.Build(context.Background())
	if err != nil {
		t.Fatalf("Build (concurrent): %v", err)
	}

	if len(seqReport.Written) != n || len(concReport.Written) != n {
		t.Fatalf("Written lengths = seq %d, conc %d, want %d each", len(seqReport.Written), len(concReport.Written), n)
	}

	for i := range seqReport.Written {
		seqRel, err := filepath.Rel(seqOut, seqReport.Written[i])
		if err != nil {
			t.Fatalf("Rel(seq): %v", err)
		}
		concRel, err := filepath.Rel(concOut, concReport.Written[i])
		if err != nil {
			t.Fatalf("Rel(conc): %v", err)
		}
		if seqRel != concRel {
			t.Fatalf("Written order mismatch at index %d: sequential = %s, concurrent = %s", i, seqRel, concRel)
		}

		seqContent := readFile(t, seqReport.Written[i])
		concContent := readFile(t, concReport.Written[i])
		if seqContent != concContent {
			t.Fatalf("output mismatch at index %d: sequential = %q, concurrent = %q", i, seqContent, concContent)
		}
	}
}

// TestBuild_DegradedRenderIsNotWritten is the I1 regression. The HTTP handler
// refuses to cache a degraded render because caching pins one request's transient
// failure in front of every later one; a static file has no TTL at all, so writing a
// degraded render pins it until the next build. It must be recorded, not written.
func TestBuild_DegradedRenderIsNotWritten(t *testing.T) {
	out := resolvedTempDir(t)
	good := newTestPage("about", types.StrategyStatic, map[string]string{"en": "/about"})
	bad := newTestPage("home", types.StrategyStatic, map[string]string{"en": "/home"})
	app := &fakeRenderer{
		pages:   []*types.Page{good, bad},
		degrade: map[string]string{renderKey("/home", "en"): "sidebar"},
	}

	b, err := New(app, Options{OutDir: out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())

	if !errors.Is(err, ErrDegradedRender) {
		t.Fatalf("Build error = %v, want ErrDegradedRender", err)
	}
	if len(report.Errors) != 1 || !errors.Is(report.Errors[0], ErrDegradedRender) {
		t.Fatalf("Report.Errors = %v, want one ErrDegradedRender", report.Errors)
	}
	// The recorded error has to say which fragment failed, or an operator reading
	// the report learns only that something did.
	if !strings.Contains(report.Errors[0].Error(), "sidebar") ||
		!strings.Contains(report.Errors[0].Error(), errFragmentFailed.Error()) {
		t.Errorf("Report.Errors[0] = %q, want it to name the failed fragment and its error", report.Errors[0])
	}
	if _, statErr := os.Stat(filepath.Join(out, "home", "index.html")); !os.IsNotExist(statErr) {
		t.Errorf("the degraded page was written to disk: stat error = %v, want not-exist", statErr)
	}
	// The rest of the build still happens: one bad page does not withhold the site.
	if len(report.Written) != 1 || report.Written[0] != filepath.Join(out, "about", "index.html") {
		t.Errorf("Report.Written = %v, want only the healthy page", report.Written)
	}
}

// TestBuild_DegradedRenderIsWrittenWithAllowDegraded checks the explicit opt-out:
// with Options.AllowDegraded the page reaches disk, and the build still succeeds.
func TestBuild_DegradedRenderIsWrittenWithAllowDegraded(t *testing.T) {
	out := resolvedTempDir(t)
	page := newTestPage("home", types.StrategyStatic, map[string]string{"en": "/home"})
	app := &fakeRenderer{
		pages:   []*types.Page{page},
		degrade: map[string]string{renderKey("/home", "en"): "sidebar"},
	}

	b, err := New(app, Options{OutDir: out, AllowDegraded: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	target := filepath.Join(out, "home", "index.html")
	if got := readFile(t, target); got != "<html>en|/home</html>" {
		t.Fatalf("content = %q", got)
	}
	if len(report.Written) != 1 || report.Written[0] != target {
		t.Fatalf("Report.Written = %v, want %q", report.Written, target)
	}
}

// TestBuild_EmptyRenderIsNotWritten covers the other half: an optional root fragment
// that fails with no fallback renders nil HTML and a nil error, which used to reach
// disk as a zero-byte index.html and an exit status of zero. It is refused even with
// AllowDegraded, which is about a partial page rather than an absent one.
func TestBuild_EmptyRenderIsNotWritten(t *testing.T) {
	for _, allowDegraded := range []bool{false, true} {
		t.Run(fmt.Sprintf("AllowDegraded=%v", allowDegraded), func(t *testing.T) {
			out := resolvedTempDir(t)
			page := newTestPage("home", types.StrategyStatic, map[string]string{"en": "/home"})
			app := &fakeRenderer{
				pages: []*types.Page{page},
				empty: map[string]bool{renderKey("/home", "en"): true},
			}

			b, err := New(app, Options{OutDir: out, AllowDegraded: allowDegraded})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			report, err := b.Build(context.Background())

			if !errors.Is(err, ErrEmptyRender) {
				t.Fatalf("Build error = %v, want ErrEmptyRender", err)
			}
			if len(report.Written) != 0 {
				t.Errorf("Report.Written = %v, want nothing written", report.Written)
			}
			if _, statErr := os.Stat(filepath.Join(out, "home", "index.html")); !os.IsNotExist(statErr) {
				t.Errorf("a zero-byte page was written: stat error = %v, want not-exist", statErr)
			}
		})
	}
}

// TestBuild_PanicInOnePageDoesNotKillTheBuild is the I5 regression for the build
// side. A build worker was a bare goroutine, so a panic anywhere under it was
// process-fatal — worst under Options.Clean, which has already emptied OutDir by
// then. It must be recorded against the page and the rest of the build must finish.
func TestBuild_PanicInOnePageDoesNotKillTheBuild(t *testing.T) {
	out := resolvedTempDir(t)
	first := newTestPage("home", types.StrategyStatic, map[string]string{"en": "/home"})
	exploding := newTestPage("about", types.StrategyStatic, map[string]string{"en": "/about"})
	last := newTestPage("contact", types.StrategyStatic, map[string]string{"en": "/contact"})
	app := &fakeRenderer{
		pages:  []*types.Page{first, exploding, last},
		panics: map[string]bool{renderKey("/about", "en"): true},
	}

	b, err := New(app, Options{OutDir: out, Clean: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())

	if !errors.Is(err, ErrBuildPanic) {
		t.Fatalf("Build error = %v, want ErrBuildPanic", err)
	}
	if len(report.Errors) != 1 || !errors.Is(report.Errors[0], ErrBuildPanic) {
		t.Fatalf("Report.Errors = %v, want one ErrBuildPanic", report.Errors)
	}
	if !strings.Contains(report.Errors[0].Error(), `page "about"`) {
		t.Errorf("Report.Errors[0] = %q, want it to name the page that panicked", report.Errors[0])
	}
	want := []string{
		filepath.Join(out, "home", "index.html"),
		filepath.Join(out, "contact", "index.html"),
	}
	if len(report.Written) != len(want) {
		t.Fatalf("Report.Written = %v, want %v", report.Written, want)
	}
	for i, path := range want {
		if report.Written[i] != path {
			t.Fatalf("Report.Written[%d] = %q, want %q", i, report.Written[i], path)
		}
		readFile(t, path)
	}
}
