package collagetest_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

// A handler registered in WithSetup answers.
func TestSite_WithSetupHandle(t *testing.T) {
	site := collagetest.NewSite(t, collagetest.WithSetup(func(app *collage.App) error {
		return app.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "api "+r.URL.Path)
		}))
	}))
	res := site.Get("/api/ping").WantStatus(http.StatusOK)
	if res.Body != "api /api/ping" {
		t.Errorf("body = %q", res.Body)
	}
}

// A mount registered in WithSetup serves its files.
func TestSite_WithSetupMount(t *testing.T) {
	site := collagetest.NewSite(t, collagetest.WithSetup(func(app *collage.App) error {
		return app.Mount("/static/", fstest.MapFS{"a.txt": {Data: []byte("file a")}})
	}))
	res := site.Get("/static/a.txt").WantStatus(http.StatusOK)
	if res.Body != "file a" {
		t.Errorf("body = %q", res.Body)
	}
}

// A not-found page registered in WithSetup is the one served.
func TestSite_WithSetupErrorPage(t *testing.T) {
	site := collagetest.NewSite(t,
		collagetest.WithTemplates(map[string]string{"missing.html": `<p>nothing here</p>`}),
		collagetest.WithSetup(func(app *collage.App) error {
			return app.RegisterNotFoundPage(collage.NewPage("missing").
				WithLayouts(collage.NewFragment("layout", "layout.html").Build()).
				WithContent(collage.NewFragment("missing", "missing.html").Build()).
				Build())
		}),
	)
	res := site.Get("/nowhere").WantStatus(http.StatusNotFound)
	if !strings.Contains(res.Body, "<p>nothing here</p>") {
		t.Errorf("body = %q, want the registered not-found page", res.Body)
	}
}

// Several WithSetup options run in order, after the default registrations and
// before Start.
func TestSite_WithSetupOrder(t *testing.T) {
	var calls []string
	step := func(name string) collagetest.SiteOption {
		return collagetest.WithSetup(func(app *collage.App) error {
			if _, ok := app.Page("home"); !ok {
				t.Errorf("%s: the home page is not registered yet", name)
			}
			calls = append(calls, name)
			return app.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Add("X-Order", name)
					next.ServeHTTP(w, r)
				})
			})
		})
	}
	site := collagetest.NewSite(t, step("first"), step("second"))
	if got := strings.Join(calls, ","); got != "first,second" {
		t.Errorf("setup calls = %q, want first,second", got)
	}
	res := site.Get("/").WantStatus(http.StatusOK)
	if got := strings.Join(res.Header.Values("X-Order"), ","); got != "first,second" {
		t.Errorf("X-Order = %q, want first,second", got)
	}
}

// A setup error is returned by TrySite, and the setups after it do not run.
func TestSite_WithSetupError(t *testing.T) {
	boom := errors.New("setup boom")
	later := false
	site, err := collagetest.TrySite(t,
		collagetest.WithSetup(func(*collage.App) error { return boom }),
		collagetest.WithSetup(func(*collage.App) error { later = true; return nil }),
	)
	if !errors.Is(err, boom) || site != nil {
		t.Errorf("TrySite = %v, %v; want nil and the setup error", site, err)
	}
	if later {
		t.Error("a setup after the failing one ran")
	}
}

// failingShutdown is a plugin whose Shutdown fails, counting its calls.
type failingShutdown struct{ calls atomic.Int32 }

func (p *failingShutdown) Name() string                             { return "test/failing-shutdown" }
func (p *failingShutdown) Version() string                          { return "0" }
func (p *failingShutdown) Init(context.Context, collage.Host) error { return nil }
func (p *failingShutdown) Shutdown(context.Context) error {
	p.calls.Add(1)
	return errShutdownBoom
}

var errShutdownBoom = errors.New("shutdown boom")

// cleanupTB is a testing.TB whose Cleanup functions are kept for the test to
// run, and whose Errorf is recorded instead of failing the test it wraps.
type cleanupTB struct {
	testing.TB
	mu       sync.Mutex
	cleanups []func()
	errs     []string
}

func (c *cleanupTB) Helper() {}

func (c *cleanupTB) Cleanup(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanups = append(c.cleanups, f)
}

// any: Errorf is testing.TB's method, and its signature is its.
func (c *cleanupTB) Errorf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errs = append(c.errs, fmt.Sprintf(format, args...))
}

// runCleanups runs the kept Cleanup functions, last first, as testing does,
// and returns what they reported.
func (c *cleanupTB) runCleanups() []string {
	c.mu.Lock()
	cleanups := c.cleanups
	c.cleanups = nil
	c.mu.Unlock()
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.errs
}

// Site.Shutdown returns the application's shutdown error and leaves nothing for
// the cleanup to do: the error is not reported a second time.
func TestSite_Shutdown(t *testing.T) {
	plugin := &failingShutdown{}
	fake := &cleanupTB{TB: t}
	site := collagetest.NewSite(fake, collagetest.WithPlugins(plugin))
	if err := site.Shutdown(context.Background()); !errors.Is(err, errShutdownBoom) {
		t.Errorf("Shutdown() = %v, want the plugin's error", err)
	}
	if errs := fake.runCleanups(); len(errs) != 0 {
		t.Errorf("the cleanup reported %q after Site.Shutdown", errs)
	}
	if n := plugin.calls.Load(); n != 1 {
		t.Errorf("plugin Shutdown calls = %d, want 1", n)
	}
}

// Without Site.Shutdown, the cleanup shuts the application down and reports
// its error.
func TestSite_ShutdownErrorAtCleanup(t *testing.T) {
	plugin := &failingShutdown{}
	fake := &cleanupTB{TB: t}
	collagetest.NewSite(fake, collagetest.WithPlugins(plugin))
	errs := fake.runCleanups()
	if len(errs) != 1 || !strings.Contains(errs[0], "shutdown boom") {
		t.Errorf("the cleanup reported %q, want the shutdown error once", errs)
	}
	if n := plugin.calls.Load(); n != 1 {
		t.Errorf("plugin Shutdown calls = %d, want 1", n)
	}
}
