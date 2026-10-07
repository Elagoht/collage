package build

import (
	"context"
	"errors"
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
		out[p] = types.CapturedResponse{
			Status:      status,
			OtherStatus: c.other[p],
			Headers:     http.Header{"X-Path": {p}},
			Unstable:    []string{"X-Nonce"},
		}
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

// TestBuild_CaptureWarnsWithoutABuildFinisher: an application that captures but
// has no plugins to finish the build still reports what the capture found.
func TestBuild_CaptureWarnsWithoutABuildFinisher(t *testing.T) {
	out := resolvedTempDir(t)
	app := newCapturingRenderer(t)
	b, err := New(app, Options{OutDir: out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !slices.ContainsFunc(report.Findings, func(f types.Finding) bool { return f.Rule == "unstable-header" }) {
		t.Errorf("Findings = %+v, want an unstable-header warning", report.Findings)
	}
}

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
		b, err := New(app, Options{OutDir: resolvedTempDir(t)})
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
	finisher := &cancelOnCapture{capturingRenderer: app, cancel: cancel}
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

// cancelOnCapture cancels the build's context as the capture begins.
type cancelOnCapture struct {
	*capturingRenderer
	cancel context.CancelFunc
}

func (c *cancelOnCapture) CaptureResponses(ctx context.Context, paths []string) (map[string]types.CapturedResponse, error) {
	c.cancel()
	return c.capturingRenderer.CaptureResponses(ctx, paths)
}
