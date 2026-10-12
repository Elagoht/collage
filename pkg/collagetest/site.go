package collagetest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// Site is a real collage application built for a test: the app, a Client on
// its handler, and everything it logged.
//
// It is for a plugin's tests, which need an application to run the plugin in
// and nothing about it in particular:
//
//	func TestRobots(t *testing.T) {
//		site := collagetest.NewSite(t,
//			collagetest.WithPlugins(robots.New(robots.Options{})),
//			collagetest.WithPluginConfig("elagoht/robots", `{"disallow":["/admin"]}`),
//		)
//		res := site.Get("/robots.txt").WantStatus(http.StatusOK)
//		if !strings.Contains(res.Body, "Disallow: /admin") {
//			t.Errorf("robots.txt = %q", res.Body)
//		}
//	}
//
// The application is collage's own, built with New and started with Start, not
// a stand-in: a plugin that passes against a Site passes against an application.
type Site struct {
	// App is the application, started. A test that shuts it down itself calls
	// Site.Shutdown, not App.Shutdown: App.Shutdown returns its first error on
	// every call, so the cleanup would report that error again.
	App *collage.App
	// Client sends requests to App's handler; its methods are the Site's.
	*Client
	// Logs is everything App logged.
	Logs *Logs

	// closed is set by Shutdown, so the cleanup NewSite registered does not
	// shut App down again and report the same error a second time.
	closed atomic.Bool
}

// SiteOption changes what NewSite and TrySite build.
type SiteOption func(*siteConfig)

// siteConfig is what the options collected.
type siteConfig struct {
	plugins      []collage.Plugin
	pluginConfig map[string]json.RawMessage
	badConfig    []string
	templates    map[string]string
	register     []collage.Registrable
	withoutHome  bool
	edits        []func(*collage.Config)
	setup        []func(*collage.App) error
}

// The templates a Site starts with, under the template root "t": a layout with
// the head hoist and the content slot a scaffolded project's layout has, and the
// home page's content.
const (
	siteLayout = `<!doctype html><html><head>{{hoist "head"}}</head><body>{{slot "content"}}</body></html>`
	siteHome   = `<p>home</p>`
)

// WithPlugins adds plugins to the application, in order.
func WithPlugins(plugins ...collage.Plugin) SiteOption {
	return func(c *siteConfig) { c.plugins = append(c.plugins, plugins...) }
}

// WithPluginConfig sets one plugin's section of the plugin configuration, as
// plugins-config.json would:
//
//	collagetest.WithPluginConfig("elagoht/robots", `{"disallow":["/admin"]}`)
//
// Invalid JSON fails the test, from TrySite as from NewSite: it is the test's own
// mistake, not the failure TrySite is for.
func WithPluginConfig(name, json string) SiteOption {
	raw := []byte(json)
	return func(c *siteConfig) { c.setPluginConfig(name, raw) }
}

// setPluginConfig records raw as name's section, or name as invalid when raw is
// not one JSON value. It is WithPluginConfig's body, outside it because that
// function's parameter, named for what it holds, shadows the json package.
func (c *siteConfig) setPluginConfig(name string, raw []byte) {
	if !json.Valid(raw) {
		c.badConfig = append(c.badConfig, name)
		return
	}
	if c.pluginConfig == nil {
		c.pluginConfig = make(map[string]json.RawMessage)
	}
	c.pluginConfig[name] = raw
}

// WithTemplates adds or replaces templates by their name under the template
// root: "layout.html" replaces the default layout, "post.html" adds a template a
// page registered with WithRegister renders.
func WithTemplates(files map[string]string) SiteOption {
	return func(c *siteConfig) { maps.Copy(c.templates, files) }
}

// WithRegister registers pages, documents and actions after the default home
// page.
func WithRegister(items ...collage.Registrable) SiteOption {
	return func(c *siteConfig) { c.register = append(c.register, items...) }
}

// WithoutHome leaves out the default home page, so / is the test's to route or
// to leave unrouted.
func WithoutHome() SiteOption {
	return func(c *siteConfig) { c.withoutHome = true }
}

// WithConfig edits the configuration before the application is built — locales,
// a cache, BaseURL, development mode, anything — after the Site's defaults and
// the other options have been applied to it. An edit that replaces Logger leaves
// Logs empty, and one that replaces Template.FS takes WithTemplates' templates
// with it.
func WithConfig(edit func(*collage.Config)) SiteOption {
	return func(c *siteConfig) { c.edits = append(c.edits, edit) }
}

// WithSetup registers what a test needs on the application itself before it
// starts — a handler, a mount, middleware, an error page:
//
//	collagetest.WithSetup(func(app *collage.App) error {
//		return app.Handle("/api/", api)
//	})
//
// setup runs after New and the default registrations (the home page and
// WithRegister's items), before Start. Several WithSetup options run in the
// order given; an error stops there and is returned by TrySite, or fails the
// test from NewSite.
func WithSetup(setup func(app *collage.App) error) SiteOption {
	return func(c *siteConfig) { c.setup = append(c.setup, setup) }
}

// NewSite builds the application, starts it — plugins' Configure and Init run
// here, so their errors fail the test at once — and shuts it down when the test
// ends.
//
// With no options, the application listens nowhere and is configured as little
// as an application can be: Server is localhost:3000, the templates are a layout
// with a head hoist and a content slot and a home page's content, the page "home"
// is served at /, the logger writes text records at debug level into Logs, and
// there is no cache, no development mode, no BaseURL and only the default
// locale. A test opts into the rest with WithConfig.
func NewSite(t testing.TB, options ...SiteOption) *Site {
	t.Helper()
	site, err := TrySite(t, options...)
	if err != nil {
		t.Fatalf("collagetest: NewSite: %v", err)
		return nil
	}
	return site
}

// TrySite is NewSite for a test that expects building or starting to fail: it
// returns the error instead of failing the test. A Site it returns shuts down
// when the test ends, as NewSite's does.
func TrySite(t testing.TB, options ...SiteOption) (*Site, error) {
	t.Helper()
	sc := &siteConfig{templates: map[string]string{"layout.html": siteLayout, "home.html": siteHome}}
	for _, option := range options {
		option(sc)
	}
	if len(sc.badConfig) > 0 {
		t.Fatalf("collagetest: WithPluginConfig: the configuration of %s is not valid JSON", strings.Join(sc.badConfig, ", "))
		return nil, nil
	}

	fsys := make(fstest.MapFS, len(sc.templates))
	for name, source := range sc.templates {
		fsys["t/"+name] = &fstest.MapFile{Data: []byte(source)}
	}
	logs := &Logs{}
	cfg := &collage.Config{
		Server:       collage.ServerConfig{Host: "localhost", Port: 3000},
		Template:     collage.TemplateConfig{FS: fsys, Root: "t"},
		Logger:       slog.New(&logsHandler{logs: logs, inner: slog.NewTextHandler(logsWriter{logs}, &slog.HandlerOptions{Level: slog.LevelDebug})}),
		Plugins:      sc.plugins,
		PluginConfig: sc.pluginConfig,
	}
	for _, edit := range sc.edits {
		edit(cfg)
	}

	app, err := collage.New(cfg)
	if err != nil {
		return nil, err
	}
	var items []collage.Registrable
	if !sc.withoutHome {
		locale := cfg.Locale.Default
		if locale == "" {
			locale = "en"
		}
		items = append(items, collage.NewPage("home").
			WithLayouts(collage.NewFragment("layout", "layout.html").Build()).
			WithContent(collage.NewFragment("home", "home.html").Build()).
			WithPath(locale, "/").
			Build())
	}
	if err := app.Register(append(items, sc.register...)...); err != nil {
		return nil, err
	}
	for _, setup := range sc.setup {
		if err := setup(app); err != nil {
			return nil, err
		}
	}
	if err := app.Start(); err != nil {
		return nil, err
	}
	site := &Site{App: app, Client: New(t, app.Handler()), Logs: logs}
	t.Cleanup(func() {
		if site.closed.Load() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := app.Shutdown(ctx); err != nil {
			t.Errorf("collagetest: shutting the site down: %v", err)
		}
	})
	return site, nil
}

// Shutdown shuts the application down, with ctx bounding the wait, and returns
// what App.Shutdown returned. The site is then closed: the cleanup NewSite
// registered does nothing more, so an error the test expects is not reported
// again when the test ends.
//
// Call site.Shutdown, not site.App.Shutdown, when the test shuts down itself.
func (s *Site) Shutdown(ctx context.Context) error {
	s.closed.Store(true)
	return s.App.Shutdown(ctx)
}

// Logs is everything the application logged, at debug level and above, as
// text records. It is safe to read while the application runs.
type Logs struct {
	mu      sync.Mutex
	records []logRecord
	// pending is the record being written: the bytes the text handler writes
	// while Handle holds mu.
	pending bytes.Buffer
}

// logRecord is one record: its level, and the line the text handler wrote.
type logRecord struct {
	level slog.Level
	text  string
}

// String returns every record, one line each, in the order they were logged.
func (l *Logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	for _, r := range l.records {
		b.WriteString(r.text)
	}
	return b.String()
}

// Has reports whether a record at level or above contains every one of parts:
//
//	site.Logs.Has(slog.LevelWarn, "robots:", "disallow=/admin")
func (l *Logs) Has(level slog.Level, parts ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.records {
		if r.level < level {
			continue
		}
		all := true
		for _, part := range parts {
			if !strings.Contains(r.text, part) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// logsWriter is where the text handler writes: the record Handle is holding
// the lock for.
type logsWriter struct{ logs *Logs }

func (w logsWriter) Write(p []byte) (int, error) { return w.logs.pending.Write(p) }

// logsHandler formats records with a text handler and keeps each, with its
// level, in Logs. Handle holds Logs' lock while the text handler writes, so
// the bytes written are this record's alone.
type logsHandler struct {
	logs  *Logs
	inner slog.Handler
}

func (h *logsHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *logsHandler) Handle(ctx context.Context, r slog.Record) error {
	h.logs.mu.Lock()
	defer h.logs.mu.Unlock()
	h.logs.pending.Reset()
	if err := h.inner.Handle(ctx, r); err != nil {
		return fmt.Errorf("collagetest: logging: %w", err)
	}
	h.logs.records = append(h.logs.records, logRecord{level: r.Level, text: h.logs.pending.String()})
	return nil
}

func (h *logsHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &logsHandler{logs: h.logs, inner: h.inner.WithAttrs(attrs)}
}

func (h *logsHandler) WithGroup(name string) slog.Handler {
	return &logsHandler{logs: h.logs, inner: h.inner.WithGroup(name)}
}
