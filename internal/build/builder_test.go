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
	f.mu.Lock()
	f.calls = append(f.calls, renderCall{path: path, locale: locale, params: params})
	failErr, shouldFail := f.fail[renderKey(path, locale)]
	delay := f.delays[renderKey(path, locale)]
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if shouldFail {
		return nil, failErr
	}

	return &render.Result{
		HTML: fmt.Appendf(nil, "<html>%s|%s</html>", locale, path),
	}, nil
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
// check.
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
