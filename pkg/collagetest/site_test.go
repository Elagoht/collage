package collagetest_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

// robotsConfig is robotsPlugin's section of the plugin configuration.
type robotsConfig struct {
	Disallow []string `json:"disallow"`
}

// robotsPlugin serves /robots.txt from its configuration, warns at Init, and
// fails Init when told to.
type robotsPlugin struct {
	fail     error
	shutdown atomic.Bool
}

func (p *robotsPlugin) Name() string    { return "test/robots" }
func (p *robotsPlugin) Version() string { return "0" }
func (p *robotsPlugin) Init(_ context.Context, host collage.Host) error {
	if p.fail != nil {
		return p.fail
	}
	cfg, err := collage.PluginConfig(host, robotsConfig{})
	if err != nil {
		return err
	}
	host.Logger().Warn("robots: serving", "disallow", strings.Join(cfg.Disallow, ","))
	body := "User-agent: *\n"
	for _, path := range cfg.Disallow {
		body += "Disallow: " + path + "\n"
	}
	return host.RegisterDocument(collage.NewDocument("robots", "text/plain; charset=utf-8").
		AtRoot("/robots.txt").WithBody([]byte(body)).Build())
}
func (p *robotsPlugin) Shutdown(context.Context) error {
	p.shutdown.Store(true)
	return nil
}

// A plugin given to WithPlugins is initialised and serves its document, and the
// default home page answers.
func TestSite_ServesAPlugin(t *testing.T) {
	site := collagetest.NewSite(t, collagetest.WithPlugins(&robotsPlugin{}))

	site.Get("/").WantStatus(http.StatusOK)
	if res := site.Get("/").WantStatus(http.StatusOK); !strings.Contains(res.Body, "<p>home</p>") {
		t.Errorf("home = %q, want the default home page", res.Body)
	}
	res := site.Get("/robots.txt").WantStatus(http.StatusOK)
	if res.Body != "User-agent: *\n" {
		t.Errorf("robots.txt = %q", res.Body)
	}
	if site.App == nil {
		t.Error("Site.App is nil")
	}
}

// WithPluginConfig is the plugin's section, read by PluginConfig in Init.
func TestSite_PluginConfig(t *testing.T) {
	site := collagetest.NewSite(t,
		collagetest.WithPlugins(&robotsPlugin{}),
		collagetest.WithPluginConfig("test/robots", `{"disallow":["/admin"]}`),
	)
	res := site.Get("/robots.txt").WantStatus(http.StatusOK)
	if !strings.Contains(res.Body, "Disallow: /admin\n") {
		t.Errorf("robots.txt = %q, want the configured Disallow", res.Body)
	}
}

// fatalTB is a testing.TB whose Fatal is recorded instead of failing the test
// it wraps; like the real one, it stops the goroutine that called it.
type fatalTB struct {
	testing.TB
	mu    sync.Mutex
	fatal string
}

func (f *fatalTB) Helper() {}

// any: Fatal and Fatalf are testing.TB's methods, and their signatures are its.
func (f *fatalTB) Fatal(args ...any) {
	f.record(fmt.Sprint(args...))
}
func (f *fatalTB) Fatalf(format string, args ...any) {
	f.record(fmt.Sprintf(format, args...))
}
func (f *fatalTB) record(msg string) {
	f.mu.Lock()
	f.fatal = msg
	f.mu.Unlock()
	runtime.Goexit()
}

// run calls fn with f on a goroutine of its own, so a Fatal ends that goroutine
// rather than the test's, and returns what Fatal was called with.
func (f *fatalTB) run(fn func(testing.TB)) string {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(f)
	}()
	<-done
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fatal
}

// TrySite returns a plugin's Init error; NewSite fails the test with it.
func TestSite_InitError(t *testing.T) {
	boom := errors.New("boom")
	site, err := collagetest.TrySite(t, collagetest.WithPlugins(&robotsPlugin{fail: boom}))
	if !errors.Is(err, boom) || site != nil {
		t.Errorf("TrySite = %v, %v; want nil and the Init error", site, err)
	}

	fake := &fatalTB{TB: t}
	msg := fake.run(func(tb testing.TB) {
		collagetest.NewSite(tb, collagetest.WithPlugins(&robotsPlugin{fail: boom}))
	})
	if !strings.Contains(msg, "boom") {
		t.Errorf("NewSite failed the test with %q, want the Init error", msg)
	}
}

// Configuration that is not JSON is the test's own mistake, and fails it.
func TestSite_InvalidPluginConfig(t *testing.T) {
	fake := &fatalTB{TB: t}
	msg := fake.run(func(tb testing.TB) {
		collagetest.TrySite(tb, collagetest.WithPluginConfig("test/robots", `{disallow`))
	})
	if !strings.Contains(msg, "test/robots") {
		t.Errorf("TrySite failed the test with %q, want the invalid configuration named", msg)
	}
}

// Logs holds what the plugin logged, and reading it while the application
// serves is race-free.
func TestSite_Logs(t *testing.T) {
	site := collagetest.NewSite(t,
		collagetest.WithPlugins(&robotsPlugin{}),
		collagetest.WithPluginConfig("test/robots", `{"disallow":["/admin"]}`),
	)
	if !site.Logs.Has(slog.LevelWarn, "robots: serving", "disallow=/admin") {
		t.Errorf("Logs lack the plugin's warning:\n%s", site.Logs)
	}
	if site.Logs.Has(slog.LevelError, "robots: serving") {
		t.Error("Has found a warning at error level")
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				site.App.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing", nil))
			}
		})
	}
	for range 20 {
		site.Logs.Has(slog.LevelDebug, "missing")
		_ = site.Logs.String()
	}
	wg.Wait()
	if !site.Logs.Has(slog.LevelDebug, "/missing") {
		t.Errorf("Logs lack the debug record of a route miss:\n%s", site.Logs)
	}
	if !strings.Contains(site.Logs.String(), "level=WARN") {
		t.Errorf("String() = %q, want text records", site.Logs.String())
	}
}

// WithoutHome leaves / unrouted; WithRegister's pages are served; WithTemplates
// replaces a default template.
func TestSite_RoutesAndTemplates(t *testing.T) {
	bare := collagetest.NewSite(t, collagetest.WithoutHome())
	bare.Get("/").WantStatus(http.StatusNotFound)

	about := collage.NewPage("about").
		WithContent(collage.NewFragment("about", "about.html").Build()).
		WithPath("en", "/about").
		Build()
	site := collagetest.NewSite(t,
		collagetest.WithTemplates(map[string]string{
			"layout.html": `<main class="replaced">{{slot "content"}}</main>`,
			"about.html":  `<p>about</p>`,
		}),
		collagetest.WithRegister(about),
	)
	res := site.Get("/about").WantStatus(http.StatusOK)
	if !strings.Contains(res.Body, "<p>about</p>") {
		t.Errorf("about = %q", res.Body)
	}
	if home := site.Get("/").WantStatus(http.StatusOK); !strings.Contains(home.Body, `<main class="replaced"><p>home</p></main>`) {
		t.Errorf("home = %q, want the replaced layout", home.Body)
	}
}

// WithConfig edits the configuration before the application is built.
func TestSite_WithConfig(t *testing.T) {
	site := collagetest.NewSite(t, collagetest.WithConfig(func(cfg *collage.Config) {
		cfg.BaseURL = "https://example.com"
	}))
	if got := site.App.BaseURL(); got != "https://example.com" {
		t.Errorf("BaseURL() = %q", got)
	}
}

// The application shuts down when the test ends, and its plugins with it.
func TestSite_ShutdownAtCleanup(t *testing.T) {
	plugin := &robotsPlugin{}
	t.Run("site", func(t *testing.T) {
		collagetest.NewSite(t, collagetest.WithPlugins(plugin))
		if plugin.shutdown.Load() {
			t.Error("the plugin shut down before the test ended")
		}
	})
	if !plugin.shutdown.Load() {
		t.Error("the plugin's Shutdown did not run at cleanup")
	}
}

// mountingPlugin mounts a file system at /docs/ in Init, swallowing any page
// under it, which fails the start after Init succeeded.
type mountingPlugin struct{ shutdown atomic.Int32 }

func (p *mountingPlugin) Name() string    { return "test/mount" }
func (p *mountingPlugin) Version() string { return "0" }
func (p *mountingPlugin) Init(_ context.Context, host collage.Host) error {
	return host.Mount("/docs/", fstest.MapFS{"a.txt": {Data: []byte("a")}})
}
func (p *mountingPlugin) Shutdown(context.Context) error {
	p.shutdown.Add(1)
	return nil
}

// A start that fails after Init leaves no plugin running: TrySite returns the
// error, and the plugin has been shut down, once.
func TestSite_FailureAfterInitLeavesNothingRunning(t *testing.T) {
	plugin := &mountingPlugin{}
	page := collage.NewPage("doc").
		WithContent(collage.NewFragment("doc", "home.html").Build()).
		WithPath("en", "/docs/x").
		Build()
	t.Run("site", func(t *testing.T) {
		site, err := collagetest.TrySite(t, collagetest.WithPlugins(plugin), collagetest.WithRegister(page))
		if !errors.Is(err, collage.ErrMountShadowsRoute) || site != nil {
			t.Fatalf("TrySite = %v, %v; want ErrMountShadowsRoute", site, err)
		}
		if plugin.shutdown.Load() != 1 {
			t.Errorf("plugin Shutdown calls after the failed start = %d, want 1", plugin.shutdown.Load())
		}
	})
	if plugin.shutdown.Load() != 1 {
		t.Errorf("plugin Shutdown calls after the test = %d, want 1", plugin.shutdown.Load())
	}
}

// WaitWarmed returns once the pages an invalidation reached are warmed again,
// so the next request is a HIT rather than a race with the warmer.
func TestSite_WaitWarmed(t *testing.T) {
	site := collagetest.NewSite(t, collagetest.WithConfig(func(cfg *collage.Config) {
		cfg.Cache = collage.CacheConfig{Enabled: true, Type: "memory"}
	}))
	site.Get("/").WantStatus(http.StatusOK)
	if err := site.App.InvalidateTags(context.Background(), collage.PathTag("/")); err != nil {
		t.Fatal(err)
	}
	site.WaitWarmed()
	if got := site.Get("/").Header.Get(collage.CacheStatusHeader); got != "HIT" {
		t.Fatalf("%s = %q after WaitWarmed, want HIT", collage.CacheStatusHeader, got)
	}
}

// WaitWarmed returns at once when warming is off, and on a site with no cache.
func TestSite_WaitWarmedWithWarmingOff(t *testing.T) {
	collagetest.NewSite(t).WaitWarmed()
	collagetest.NewSite(t, collagetest.WithConfig(func(cfg *collage.Config) {
		cfg.Cache = collage.CacheConfig{Enabled: true, Type: "memory", WarmLimit: -1}
	})).WaitWarmed()
}
