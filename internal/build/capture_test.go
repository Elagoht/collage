package build

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/internal/asset"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/types"
)

// capturingRenderer is a fakeRenderer whose default locale carries its prefix, so
// the build writes a root redirect, and which answers CaptureResponses: every
// path with 200, its own path in X-Path and X-Nonce unstable, except the paths in
// status.
type capturingRenderer struct {
	*fakeRenderer
	status map[string]int
	// other is the second answer's status for a path, when it differs.
	other map[string]int
	// failed are the paths whose capture fails.
	failed map[string]bool
	// dev is what DevMode reports.
	dev bool
	// cacheControl is a path's Cache-Control, when it has one.
	cacheControl map[string]string
	// stable are the paths answered with no unstable header.
	stable map[string]bool

	mu       sync.Mutex
	captured []string
}

func (c *capturingRenderer) PrefixDefault() bool { return true }

func (c *capturingRenderer) DevMode() bool { return c.dev }

func (c *capturingRenderer) CaptureResponses(ctx context.Context, paths []string) (map[string]types.CapturedResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.captured = append(c.captured, paths...)
	c.mu.Unlock()
	out := make(map[string]types.CapturedResponse, len(paths))
	for _, p := range paths {
		status := http.StatusOK
		if s, ok := c.status[p]; ok {
			status = s
		}
		if c.failed[p] {
			out[p] = types.CapturedResponse{Err: errors.New("not answered in time")}
			continue
		}
		resp := types.CapturedResponse{
			Status:      status,
			OtherStatus: c.other[p],
			Headers:     http.Header{"X-Path": {p}},
			Unstable:    []string{"X-Nonce"},
		}
		if cc, ok := c.cacheControl[p]; ok {
			resp.Headers.Set("Cache-Control", cc)
		}
		if c.stable[p] {
			resp.Unstable = nil
		}
		out[p] = resp
	}
	return out, nil
}

// capturingFinisher is a capturingRenderer that also takes the finished build's
// event, so a test can read the files it carries.
type capturingFinisher struct {
	*capturingRenderer
	files []plugin.BuiltFile
}

func (c *capturingFinisher) BuildFinished(_ context.Context, ev *plugin.BuildFinishedEvent) error {
	c.files = ev.Files
	return nil
}

var (
	_ ResponseCapturer = (*capturingRenderer)(nil)
	_ BuildFinisher    = (*capturingFinisher)(nil)
)

func newCapturingRenderer(t *testing.T) *capturingRenderer {
	t.Helper()
	mount, err := asset.New("/static/", fstest.MapFS{
		"app.css": &fstest.MapFile{Data: []byte("body{}")},
	})
	if err != nil {
		t.Fatalf("asset.New: %v", err)
	}
	return &capturingRenderer{
		fakeRenderer: &fakeRenderer{
			pages:        []*types.Page{newTestPage("home", types.StrategyStatic, map[string]string{"en": "/"})},
			documents:    []*types.Document{newTestDocument("feed", types.StrategyStatic, map[string]string{"en": "/feed.xml"})},
			mounts:       []*asset.Mount{mount},
			notFoundHTML: "missing",
		},
		status: map[string]int{"/en/feed.xml": http.StatusNotFound},
	}
}

// TestBuild_CapturesEveryWrittenFilesHeaders: the build asks the application for
// every page, document and asset it wrote, and for neither file it made up itself
// — the root redirect and the 404 page — and what comes back lands on each file.
func TestBuild_CapturesEveryWrittenFilesHeaders(t *testing.T) {
	out := resolvedTempDir(t)
	app := &capturingFinisher{capturingRenderer: newCapturingRenderer(t)}
	b, err := New(app, Options{OutDir: out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	captured := slices.Sorted(slices.Values(app.captured))
	if want := []string{"/en", "/en/feed.xml", "/static/app.css"}; !slices.Equal(captured, want) {
		t.Fatalf("captured %v, want %v", captured, want)
	}

	var synthesized int
	for _, f := range app.files {
		if f.Path == "/" || strings.HasSuffix(f.Path, "/404.html") {
			synthesized++
			if f.Status != 0 || f.Headers != nil {
				t.Errorf("%s (synthesized) got Status %d Headers %v", f.Path, f.Status, f.Headers)
			}
			continue
		}
		if f.Headers.Get("X-Path") != f.Path {
			t.Errorf("%s Headers = %v, want its own captured headers", f.Path, f.Headers)
		}
		wantStatus := http.StatusOK
		if f.Path == "/en/feed.xml" {
			wantStatus = http.StatusNotFound
		}
		if f.Status != wantStatus {
			t.Errorf("%s Status = %d, want %d", f.Path, f.Status, wantStatus)
		}
	}
	if synthesized != 2 {
		t.Errorf("files = %+v, want the root redirect and the 404 page among them", app.files)
	}

	var unstable, status []types.Finding
	for _, f := range report.Findings {
		switch f.Rule {
		case "unstable-header":
			unstable = append(unstable, f)
		case "capture-status":
			status = append(status, f)
		}
	}
	if len(unstable) != 1 || !strings.Contains(unstable[0].Message, "X-Nonce") || !strings.Contains(unstable[0].Message, "3 path(s)") {
		t.Errorf("unstable-header findings = %+v, want one naming X-Nonce on 3 paths", unstable)
	}
	if len(status) != 1 || status[0].Path != "/en/feed.xml" || status[0].Level != types.FindingWarning {
		t.Errorf("capture-status findings = %+v, want one warning for /en/feed.xml", status)
	}
}

// TestBuild_NothingIsCapturedWithoutAReader: the captured headers are for the
// plugins that check a finished build. An application that cannot finish a
// build, or one whose finishing has no plugin behind it, is not asked for
// anything, and no capture warning is reported.
func TestBuild_NothingIsCapturedWithoutAReader(t *testing.T) {
	for name, app := range map[string]interface {
		Renderer
		ResponseCapturer
	}{
		"no BuildFinisher": newCapturingRenderer(t),
		"no hook":          &hooklessFinisher{capturingFinisher: &capturingFinisher{capturingRenderer: newCapturingRenderer(t)}},
	} {
		b, err := New(app, Options{OutDir: resolvedTempDir(t)})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		report, err := b.Build(context.Background())
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		var captured []string
		switch a := app.(type) {
		case *capturingRenderer:
			captured = a.captured
		case *hooklessFinisher:
			captured = a.captured
		}
		if len(captured) != 0 {
			t.Errorf("%s: captured %v, want nothing", name, captured)
		}
		if f := findingsBy(report, "unstable-header"); len(f) != 0 {
			t.Errorf("%s: unstable-header = %+v, want none", name, f)
		}
	}
}

// hooklessFinisher is a capturingFinisher with no plugin behind its
// BuildFinished.
type hooklessFinisher struct{ *capturingFinisher }

func (*hooklessFinisher) HasBuildFinishedHook() bool { return false }

// findingsBy returns report's findings under rule.
func findingsBy(report *Report, rule string) []types.Finding {
	var out []types.Finding
	for _, f := range report.Findings {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

// TestBuild_CaptureWarnsAboutFailuresAndFlappingStatuses: a path the capture
// could not answer is warned as capture-failed and keeps no headers; one answered
// with two statuses is warned as capture-status.
func TestBuild_CaptureWarnsAboutFailuresAndFlappingStatuses(t *testing.T) {
	app := newCapturingRenderer(t)
	app.status = nil
	app.failed = map[string]bool{"/static/app.css": true}
	app.other = map[string]int{"/en": http.StatusTooManyRequests}
	finisher := &capturingFinisher{capturingRenderer: app}
	b, err := New(finisher, Options{OutDir: resolvedTempDir(t)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if f := findingsBy(report, "capture-failed"); len(f) != 1 || f[0].Path != "/static/app.css" {
		t.Errorf("capture-failed = %+v, want one for /static/app.css", f)
	}
	if f := findingsBy(report, "capture-status"); len(f) != 1 || f[0].Path != "/en" || !strings.Contains(f[0].Message, "429") {
		t.Errorf("capture-status = %+v, want one for /en naming 429", f)
	}
	for _, f := range finisher.files {
		if f.Path == "/static/app.css" && (f.Status != 0 || f.Headers != nil) {
			t.Errorf("the failed capture set %+v", f)
		}
	}
}

// TestBuild_CaptureInDevModeWarns: headers captured in development mode are
// development's, and the build says so once.
func TestBuild_CaptureInDevModeWarns(t *testing.T) {
	for _, dev := range []bool{false, true} {
		app := newCapturingRenderer(t)
		app.dev = dev
		b, err := New(&capturingFinisher{capturingRenderer: app}, Options{OutDir: resolvedTempDir(t)})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		report, err := b.Build(context.Background())
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		want := 0
		if dev {
			want = 1
		}
		if f := findingsBy(report, "capture-dev-mode"); len(f) != want {
			t.Errorf("dev=%v: capture-dev-mode = %+v, want %d", dev, f, want)
		}
	}
}

// TestBuild_CaptureReturnsACancelledBuildsError: a capture stopped by the
// build's own context ending fails the build rather than warning.
func TestBuild_CaptureReturnsACancelledBuildsError(t *testing.T) {
	app := newCapturingRenderer(t)
	ctx, cancel := context.WithCancel(context.Background())
	finisher := &cancellingFinisher{capturingFinisher: &capturingFinisher{capturingRenderer: app}, cancel: cancel}
	b, err := New(finisher, Options{OutDir: resolvedTempDir(t)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Build = %v, want context.Canceled", err)
	}
	if f := findingsBy(report, "capture-failed"); len(f) != 0 {
		t.Errorf("capture-failed = %+v, want the error alone", f)
	}
}

// stoppingCapturer is a capturingRenderer whose capture gives up after the first
// path.
type stoppingCapturer struct{ *capturingRenderer }

func (s stoppingCapturer) CaptureResponses(_ context.Context, paths []string) (map[string]types.CapturedResponse, error) {
	return map[string]types.CapturedResponse{paths[0]: {Err: types.ErrCaptureTimeout}},
		fmt.Errorf("%w: %d path(s) left uncaptured", types.ErrCaptureStopped, len(paths)-1)
}

// TestBuild_AStoppedCaptureWarnsOnceMore: a capture that gave up is one more
// warning naming what was left, on top of the timed-out path's, and not a failed
// build.
func TestBuild_AStoppedCaptureWarnsOnceMore(t *testing.T) {
	b, err := New(&stoppingFinisher{capturingFinisher: &capturingFinisher{capturingRenderer: newCapturingRenderer(t)}}, Options{OutDir: resolvedTempDir(t)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	f := findingsBy(report, "capture-failed")
	if len(f) != 2 || f[0].Path != "" || !strings.Contains(f[0].Message, "2 path(s) left uncaptured") || f[1].Path != "/en" {
		t.Errorf("capture-failed = %+v, want the stop naming 2 paths left, then /en's timeout", f)
	}
}

// TestBuild_CapturedMarksEveryFileTheBuildAskedFor: a file the build asked the
// application for is Captured whether the answer came, failed, or was never
// reached because the capture stopped; the files the build made itself are not.
func TestBuild_CapturedMarksEveryFileTheBuildAskedFor(t *testing.T) {
	t.Run("failed", func(t *testing.T) {
		app := newCapturingRenderer(t)
		app.failed = map[string]bool{"/static/app.css": true}
		finisher := &capturingFinisher{capturingRenderer: app}
		b, err := New(finisher, Options{OutDir: resolvedTempDir(t)})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := b.Build(context.Background()); err != nil {
			t.Fatalf("Build: %v", err)
		}
		assertCaptured(t, finisher.files)
		for _, f := range finisher.files {
			if f.Path == "/static/app.css" && f.Status != 0 {
				t.Errorf("the failed capture's Status = %d, want 0", f.Status)
			}
		}
	})
	t.Run("stopped", func(t *testing.T) {
		finisher := &stoppingFinisher{capturingFinisher: &capturingFinisher{capturingRenderer: newCapturingRenderer(t)}}
		b, err := New(finisher, Options{OutDir: resolvedTempDir(t)})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := b.Build(context.Background()); err != nil {
			t.Fatalf("Build: %v", err)
		}
		assertCaptured(t, finisher.files)
		for _, f := range finisher.files {
			if f.Status != 0 {
				t.Errorf("%s Status = %d after a stopped capture, want 0", f.Path, f.Status)
			}
		}
	})
}

// assertCaptured checks that every file but the root redirect and the 404 page
// is Captured.
func assertCaptured(t *testing.T, files []plugin.BuiltFile) {
	t.Helper()
	if len(files) == 0 {
		t.Fatal("no files reached BuildFinished")
	}
	for _, f := range files {
		synthesized := f.Path == "/" || strings.HasSuffix(f.Path, "/404.html")
		if f.Captured == synthesized {
			t.Errorf("%s Captured = %v, want %v", f.Path, f.Captured, !synthesized)
		}
	}
}

// stoppingFinisher is a capturingFinisher whose capture gives up after the first
// path.
type stoppingFinisher struct{ *capturingFinisher }

func (s *stoppingFinisher) CaptureResponses(ctx context.Context, paths []string) (map[string]types.CapturedResponse, error) {
	return stoppingCapturer{s.capturingRenderer}.CaptureResponses(ctx, paths)
}

// cancellingFinisher is a capturingFinisher that cancels the build's context as
// the capture begins, and records whether BuildFinished ran.
type cancellingFinisher struct {
	*capturingFinisher
	cancel   context.CancelFunc
	finished bool
}

func (c *cancellingFinisher) CaptureResponses(ctx context.Context, paths []string) (map[string]types.CapturedResponse, error) {
	c.cancel()
	return c.capturingRenderer.CaptureResponses(ctx, paths)
}

func (c *cancellingFinisher) BuildFinished(ctx context.Context, ev *plugin.BuildFinishedEvent) error {
	c.finished = true
	return c.capturingFinisher.BuildFinished(ctx, ev)
}

// TestBuild_ACancelledBuildIsNotFinished: once the build's context has ended,
// the plugins that check a finished build are not handed the partial one; the
// build returns the context's error.
func TestBuild_ACancelledBuildIsNotFinished(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := &cancellingFinisher{capturingFinisher: &capturingFinisher{capturingRenderer: newCapturingRenderer(t)}, cancel: cancel}
	b, err := New(app, Options{OutDir: resolvedTempDir(t)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := b.Build(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Build = %v, want context.Canceled", err)
	}
	if app.finished {
		t.Error("BuildFinished ran on a cancelled build")
	}
}

// TestBuild_CaptureWarnsAboutPersonalPages: a page answered as personal —
// Cache-Control private or no-store — beside a header that differed between
// the two answers, such as a nonce, is exported without that header, so the
// file is no longer personal, and the Cache-Control would only keep a host from
// caching it. One warning names how many and a few of them; a public page with
// a nonce, and a private one with nothing unstable, are not among them. A
// PersonaliseHook's Personal no longer makes a capture private (see
// pkg/collage's TestBuild_CaptureOfAHookPersonalPageKeepsItsStrategysCacheControl),
// so the warning says the Cache-Control does not come from one.
func TestBuild_CaptureWarnsAboutPersonalPages(t *testing.T) {
	app := newCapturingRenderer(t)
	paths := []string{"/a", "/b", "/c", "/d", "/public", "/private-stable", "/quoted"}
	for _, p := range paths {
		app.pages = append(app.pages, newTestPage(strings.Trim(p, "/"), types.StrategyStatic, map[string]string{"en": p}))
	}
	app.cacheControl = map[string]string{
		"/en/a":              "private, no-store",
		"/en/b":              "no-store",
		"/en/c":              "Private",
		"/en/d":              "max-age=0, private",
		"/en/public":         "public, max-age=60",
		"/en/private-stable": "private",
		"/en/quoted":         `private="Set-Cookie", max-age=60`,
		"/en/feed.xml":       "private",
	}
	app.stable = map[string]bool{"/en/private-stable": true}
	app.status = nil
	b, err := New(&capturingFinisher{capturingRenderer: app}, Options{OutDir: resolvedTempDir(t)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	f := findingsBy(report, "capture-personal")
	if len(f) != 1 {
		t.Fatalf("capture-personal = %+v, want one", f)
	}
	msg := f[0].Message
	if f[0].Level != types.FindingWarning || f[0].Path != "" || !strings.Contains(msg, "5 page(s)") {
		t.Errorf("capture-personal = %+v, want one warning naming 5 pages", f[0])
	}
	for _, want := range []string{"no longer personal", "will not cache", "PersonaliseHook's Personal, which the capture sets aside"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not say %q", msg, want)
		}
	}
	for _, not := range []string{"/en/public", "/en/private-stable", "/en/feed.xml"} {
		if strings.Contains(msg, not) {
			t.Errorf("message %q names %s", msg, not)
		}
	}
	if !strings.Contains(msg, "/en/a") {
		t.Errorf("message %q names none of the pages", msg)
	}
}
