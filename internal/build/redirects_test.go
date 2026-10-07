package build

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/types"
)

// redirectingRenderer is a fakeRenderer whose plugins declare redirects and
// which takes the finished build's event.
type redirectingRenderer struct {
	*fakeRenderer
	plugin    []plugin.BuiltRedirect
	pluginErr error
	finished  *plugin.BuildFinishedEvent
}

func (r *redirectingRenderer) PluginRedirects() ([]plugin.BuiltRedirect, error) {
	if r.pluginErr != nil {
		return nil, r.pluginErr
	}
	return r.plugin, nil
}

func (r *redirectingRenderer) BuildFinished(_ context.Context, ev *plugin.BuildFinishedEvent) error {
	r.finished = ev
	return nil
}

var _ BuildFinisher = (*redirectingRenderer)(nil)

func newRedirectingRenderer() *redirectingRenderer {
	about := newTestPage("about", types.StrategyStatic, map[string]string{"en": "/a"})
	about.Redirects = []*types.Redirect{{From: "/old", To: "/a", StatusCode: 308}}
	blog := newTestPage("blog", types.StrategyDynamic, map[string]string{"en": "/blog"})
	blog.Redirects = []*types.Redirect{{From: "/posts", To: "/blog", Permanent: true}}
	feed := newTestDocument("feed", types.StrategyStatic, map[string]string{"en": "/feed.xml"})
	feed.Redirects = []*types.Redirect{{From: "/rss", To: "/feed.xml"}}
	return &redirectingRenderer{
		fakeRenderer: &fakeRenderer{
			pages:     []*types.Page{about, blog},
			documents: []*types.Document{feed},
		},
		plugin: []plugin.BuiltRedirect{{From: "/gone", Status: 410, Source: "legacy"}},
	}
}

func buildRedirects(t *testing.T, app Renderer) (*Report, error) {
	t.Helper()
	b, err := New(app, Options{OutDir: resolvedTempDir(t)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b.Build(context.Background())
}

// TestBuild_HandsEveryRedirectToTheHook: pages' redirects in registration order
// — a page that is not built too — then documents', then plugins', each with its
// effective status and its source.
func TestBuild_HandsEveryRedirectToTheHook(t *testing.T) {
	app := newRedirectingRenderer()
	if _, err := buildRedirects(t, app); err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []plugin.BuiltRedirect{
		{From: "/old", To: "/a", Status: 308, Source: "page:about"},
		{From: "/posts", To: "/blog", Status: 301, Source: "page:blog"},
		{From: "/rss", To: "/feed.xml", Status: 302, Source: "document:feed"},
		{From: "/gone", Status: 410, Source: "legacy"},
	}
	if app.finished == nil || !slices.Equal(app.finished.Redirects, want) {
		t.Fatalf("Redirects = %+v, want %+v", app.finished, want)
	}
}

// TestBuild_FailsOnADuplicateRedirect: a plugin rule from the same path as a
// page's fails the build, naming both.
func TestBuild_FailsOnADuplicateRedirect(t *testing.T) {
	app := newRedirectingRenderer()
	app.plugin = append(app.plugin, plugin.BuiltRedirect{From: "/old", To: "/elsewhere", Status: 301, Source: "legacy"})
	_, err := buildRedirects(t, app)
	if !errors.Is(err, ErrDuplicateRedirect) {
		t.Fatalf("Build error = %v, want ErrDuplicateRedirect", err)
	}
	if !strings.Contains(err.Error(), "page:about") || !strings.Contains(err.Error(), "legacy") {
		t.Errorf("Build error = %v, want both sources named", err)
	}
}

// TestBuild_FailsOnARedirectShadowingAFile: a rule from a written page's path,
// in either spelling, or from a written document's, fails the build.
func TestBuild_FailsOnARedirectShadowingAFile(t *testing.T) {
	for _, from := range []string{"/a/", "/a", "/feed.xml"} {
		t.Run(from, func(t *testing.T) {
			app := newRedirectingRenderer()
			app.plugin = []plugin.BuiltRedirect{{From: from, To: "/b", Status: 301, Source: "legacy"}}
			_, err := buildRedirects(t, app)
			if !errors.Is(err, ErrRedirectShadowsFile) {
				t.Fatalf("Build error = %v, want ErrRedirectShadowsFile", err)
			}
			if !strings.Contains(err.Error(), "legacy") {
				t.Errorf("Build error = %v, want the source named", err)
			}
		})
	}
}

// TestBuild_FailsOnAPluginRedirectsError: a RedirectSource whose rules do not
// validate fails the build.
func TestBuild_FailsOnAPluginRedirectsError(t *testing.T) {
	app := newRedirectingRenderer()
	app.pluginErr = types.RedirectTextError("/x\n", "/y")
	_, err := buildRedirects(t, app)
	if !errors.Is(err, types.ErrInvalidRedirect) {
		t.Fatalf("Build error = %v, want ErrInvalidRedirect", err)
	}
}

// TestBuild_FailsOnAPluginRedirectStatus: a plugin rule's status must be a
// redirect's or 410.
func TestBuild_FailsOnAPluginRedirectStatus(t *testing.T) {
	for _, status := range []int{0, 200, 404} {
		app := newRedirectingRenderer()
		app.plugin = []plugin.BuiltRedirect{{From: "/x", To: "/y", Status: status, Source: "legacy"}}
		_, err := buildRedirects(t, app)
		if !errors.Is(err, types.ErrInvalidRedirectStatus) || !strings.Contains(err.Error(), "legacy") {
			t.Errorf("status %d: Build error = %v, want ErrInvalidRedirectStatus naming legacy", status, err)
		}
	}
}

// TestBuild_ChecksRedirectsWithoutAFinisher: the checks are the build's, not a
// plugin's.
func TestBuild_ChecksRedirectsWithoutAFinisher(t *testing.T) {
	app := newRedirectingRenderer()
	app.plugin = []plugin.BuiltRedirect{{From: "/a/", To: "/b", Status: 301, Source: "legacy"}}
	_, err := buildRedirects(t, struct {
		*fakeRenderer
		*pluginRedirectsOnly
	}{app.fakeRenderer, &pluginRedirectsOnly{app}})
	if !errors.Is(err, ErrRedirectShadowsFile) {
		t.Fatalf("Build error = %v, want ErrRedirectShadowsFile", err)
	}
}

// pluginRedirectsOnly exposes only PluginRedirects of a redirectingRenderer.
type pluginRedirectsOnly struct{ r *redirectingRenderer }

func (p *pluginRedirectsOnly) PluginRedirects() ([]plugin.BuiltRedirect, error) {
	return p.r.PluginRedirects()
}
