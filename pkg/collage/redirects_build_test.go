package collage_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// legacyRules is a RedirectSource that also takes the finished build's event.
type legacyRules struct {
	rules []collage.BuiltRedirect
	built *collage.BuildFinishedEvent
}

func (*legacyRules) Name() string                             { return "test/legacy" }
func (*legacyRules) Version() string                          { return "0.0.0" }
func (*legacyRules) Init(context.Context, collage.Host) error { return nil }
func (*legacyRules) Shutdown(context.Context) error           { return nil }
func (l *legacyRules) Redirects() []collage.BuiltRedirect     { return l.rules }

func (l *legacyRules) OnBuildFinished(_ context.Context, ev *collage.BuildFinishedEvent) error {
	l.built = ev
	return nil
}

func buildLegacySite(t *testing.T, rules []collage.BuiltRedirect) (*legacyRules, error) {
	t.Helper()
	l := &legacyRules{rules: rules}
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<html><body><h1>p</h1></body></html>`)}}, Root: "t"},
		Plugins:  []collage.Plugin{l},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := collage.NewPage("about").WithContent(collage.NewFragment("about", "p.html").Build()).
		WithPath("en", "/about").WithPermanentRedirect("/about-us", "/about").Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatal(err)
	}
	// Never matched, so its redirect is never served, and not exported.
	missing := collage.NewPage("missing").WithContent(collage.NewFragment("missing", "p.html").Build()).
		WithPath("en", "/missing").WithPermanentRedirect("/lost", "/about").Build()
	if err := app.RegisterNotFoundPage(missing); err != nil {
		t.Fatal(err)
	}
	builder, err := collage.NewBuilder(app, collage.BuildOptions{OutDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = builder.Build(context.Background())
	return l, err
}

// A build hands the hook its pages' redirects and its plugins'.
func TestBuild_RedirectsReachTheHook(t *testing.T) {
	l, err := buildLegacySite(t, []collage.BuiltRedirect{{From: "/gone", Status: 410}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []collage.BuiltRedirect{
		{From: "/about-us", To: "/about", Status: 301, Source: "page:about"},
		{From: "/gone", Status: 410, Source: "test/legacy"},
	}
	if l.built == nil || !slices.Equal(l.built.Redirects, want) {
		t.Fatalf("Redirects = %+v, want %+v", l.built, want)
	}
}

// A plugin rule over a written page fails the build.
func TestBuild_PluginRedirectShadowingAPageFails(t *testing.T) {
	_, err := buildLegacySite(t, []collage.BuiltRedirect{{From: "/about", To: "/x", Status: 301}})
	if !errors.Is(err, collage.ErrRedirectShadowsFile) {
		t.Fatalf("Build = %v, want ErrRedirectShadowsFile", err)
	}
}
