package collage

import (
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

// TestBuild_CaptureLeavesNoCacheEntries: a build with a disk cache leaves the
// cache directory as it found it, though its header capture asked the handler
// for the page — and a reader's request afterwards does write there, which is
// what shows the check can fail.
func TestBuild_CaptureLeavesNoCacheEntries(t *testing.T) {
	cacheDir := t.TempDir()
	app, marked := newCaptureCountingApp(t, CacheConfig{Enabled: true, Type: "disk", Dir: cacheDir}, &buildReader{})

	builder, err := NewBuilder(app, BuildOptions{OutDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
	if _, err := builder.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if n := countFiles(t, cacheDir); n != 0 {
		t.Fatalf("the build left %d file(s) in the cache directory", n)
	}
	if len(*marked) != 2 || !(*marked)[0] || !(*marked)[1] {
		t.Fatalf("IsCapture during the build = %v, want two marked requests", *marked)
	}

	app.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if n := countFiles(t, cacheDir); n == 0 {
		t.Fatal("a reader's request wrote nothing to the cache either; the check proves nothing")
	}
	if (*marked)[2] {
		t.Error("a reader's request was marked as a capture")
	}
}

// buildReader is a plugin that reads a finished build, as a deploy adapter does.
type buildReader struct{ built *BuildFinishedEvent }

func (*buildReader) Name() string                     { return "test/buildreader" }
func (*buildReader) Version() string                  { return "0" }
func (*buildReader) Init(context.Context, Host) error { return nil }
func (*buildReader) Shutdown(context.Context) error   { return nil }
func (b *buildReader) OnBuildFinished(_ context.Context, ev *BuildFinishedEvent) error {
	b.built = ev
	return nil
}

// TestBuild_NothingIsCapturedWithoutABuildReader: with no plugin implementing
// BuildFinishedHook nothing reads the captured headers, so the build does not
// ask the handler for any path; with one, it does.
func TestBuild_NothingIsCapturedWithoutABuildReader(t *testing.T) {
	for _, withReader := range []bool{false, true} {
		var plugins []Plugin
		if withReader {
			plugins = append(plugins, &buildReader{})
		}
		app, marked := newCaptureCountingApp(t, CacheConfig{}, plugins...)
		builder, err := NewBuilder(app, BuildOptions{OutDir: t.TempDir()})
		if err != nil {
			t.Fatalf("NewBuilder: %v", err)
		}
		report, err := builder.Build(context.Background())
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		want := 0
		if withReader {
			want = 2
		}
		if len(*marked) != want {
			t.Errorf("reader=%v: %d capture requests, want %d", withReader, len(*marked), want)
		}
		for _, f := range report.Findings {
			if !withReader && strings.HasPrefix(f.Rule, "capture-") {
				t.Errorf("reader=%v: finding %+v from a capture that should not run", withReader, f)
			}
		}
	}
}

// newCaptureCountingApp is an application with one static page at "/" and a
// middleware recording, for each request, whether it was a capture.
func newCaptureCountingApp(t *testing.T, cache CacheConfig, plugins ...Plugin) (*App, *[]bool) {
	t.Helper()
	marked := &[]bool{}
	app, err := New(&Config{
		Server:   ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{Root: templateRoot(t)},
		Cache:    cache,
		Plugins:  plugins,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	layout := NewFragment("layout", "layouts/default.html").WithSlot("content", true, false).Build()
	page := NewPage("home").
		WithLayouts(layout).
		WithContent(NewFragment("home-content", "pages/home.html").Build()).
		WithPath("en", "/").
		Static().
		Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	err = app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*marked = append(*marked, IsCapture(r.Context()))
			next.ServeHTTP(w, r)
		})
	})
	if err != nil {
		t.Fatalf("Use: %v", err)
	}
	return app, marked
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return n
}

// nonceReader stamps a new nonce into every page and its header and marks the
// body Personal, as a CSP nonce plugin does, and reads the finished build.
type nonceReader struct {
	buildReader
	n atomic.Int32
}

func (*nonceReader) Name() string { return "test/noncereader" }
func (p *nonceReader) OnPersonalise(_ context.Context, ev *PersonaliseEvent) error {
	nonce := "n" + strconv.Itoa(int(p.n.Add(1)))
	ev.Body = bytes.ReplaceAll(ev.Body, []byte("NONCE"), []byte(nonce))
	ev.Header.Set("X-Nonce", nonce)
	ev.Personal = true
	return nil
}

// TestBuild_CaptureOfAHookPersonalPageKeepsItsStrategysCacheControl: a
// PersonaliseHook that marks every body Personal — a CSP nonce — does not make
// the build's capture private. The exported file has no nonce (the header is
// unstable and left out), so it is exported with the Cache-Control its strategy
// gives a page nobody personalised, and no capture-personal warning is raised.
// A reader's request to the same page is still private: the hook ran for it.
func TestBuild_CaptureOfAHookPersonalPageKeepsItsStrategysCacheControl(t *testing.T) {
	plugin := &nonceReader{}
	app := newNonceApp(t, false, plugin)
	// What an incremental page with an hour's TTL is answered with: see the
	// handler's cacheControl.
	const want = "public, max-age=3600"

	report := buildNonceApp(t, app)
	if n := plugin.n.Load(); n < 2 {
		t.Fatalf("the hook ran %d time(s) during the build, want at least the two captures", n)
	}
	var pages int
	for _, f := range plugin.built.Files {
		if f.Kind != "page" {
			continue
		}
		pages++
		if got := f.Headers.Get("Cache-Control"); got != want {
			t.Errorf("%s captured Cache-Control = %q, want the strategy's %q", f.Path, got, want)
		}
		if got := f.Headers.Get("X-Nonce"); got != "" {
			t.Errorf("%s captured X-Nonce = %q, want it left out as unstable", f.Path, got)
		}
	}
	if pages == 0 {
		t.Fatalf("no page among the built files %+v", plugin.built.Files)
	}
	for _, f := range report.Findings {
		if f.Rule == "capture-personal" {
			t.Errorf("finding %+v for a page only a hook made personal", f)
		}
	}

	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("a reader's Cache-Control = %q, want private, no-store", got)
	}
	if !strings.Contains(rec.Body.String(), "<p>n") {
		t.Errorf("a reader's body = %q, want the hook's nonce in it", rec.Body.String())
	}
}

// newNonceApp is an application with one incremental page at "/" whose body
// carries plugin's nonce.
func newNonceApp(t *testing.T, devMode bool, plugin *nonceReader) *App {
	t.Helper()
	app, err := New(&Config{
		Server:  ServerConfig{Host: "localhost", Port: 3000},
		DevMode: devMode,
		Template: TemplateConfig{FS: fstest.MapFS{
			"t/p.html": {Data: []byte(`<p>NONCE</p>`)},
		}, Root: "t"},
		Cache:   CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour},
		Plugins: []Plugin{plugin},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	page := NewPage("home").WithContent(NewFragment("p", "p.html").Build()).
		WithPath("en", "/").Incremental(time.Hour).Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	return app
}

func buildNonceApp(t *testing.T, app *App) *BuildReport {
	t.Helper()
	builder, err := NewBuilder(app, BuildOptions{OutDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
	report, err := builder.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return report
}

func findingsRuled(report *BuildReport, rule string) []Finding {
	var out []Finding
	for _, f := range report.Findings {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

// privateWriter sets Cache-Control private on whatever its handler answers, as
// a middleware that has the last word on it would.
type privateWriter struct{ http.ResponseWriter }

func (w privateWriter) WriteHeader(status int) {
	w.Header().Set("Cache-Control", "private")
	w.ResponseWriter.WriteHeader(status)
}

// TestBuild_CapturePersonalIsTheApplicationsOwnCacheControl: with the hook's
// Personal set aside, capture-personal is what a private Cache-Control the
// application sets itself raises beside a nonce — here a middleware's — and
// its message does not blame a PersonaliseHook.
func TestBuild_CapturePersonalIsTheApplicationsOwnCacheControl(t *testing.T) {
	app := newNonceApp(t, false, &nonceReader{})
	err := app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(privateWriter{w}, r)
		})
	})
	if err != nil {
		t.Fatalf("Use: %v", err)
	}
	f := findingsRuled(buildNonceApp(t, app), "capture-personal")
	if len(f) != 1 {
		t.Fatalf("capture-personal = %+v, want one", f)
	}
	for _, want := range []string{"1 page(s)", "PersonaliseHook's Personal, which the capture sets aside"} {
		if !strings.Contains(f[0].Message, want) {
			t.Errorf("message %q does not say %q", f[0].Message, want)
		}
	}
}

// TestBuild_CaptureInDevModeIsNotAlsoPersonal: a development build's pages are
// answered no-store, which capture-dev-mode already explains; beside a nonce,
// that is not a capture-personal warning too.
func TestBuild_CaptureInDevModeIsNotAlsoPersonal(t *testing.T) {
	report := buildNonceApp(t, newNonceApp(t, true, &nonceReader{}))
	if f := findingsRuled(report, "capture-dev-mode"); len(f) != 1 {
		t.Fatalf("capture-dev-mode = %+v, want one", f)
	}
	if f := findingsRuled(report, "capture-personal"); len(f) != 0 {
		t.Errorf("capture-personal = %+v in a dev-mode build", f)
	}
}
