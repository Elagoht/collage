package collage_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

// hgPlugin runs initFn in Init and configureFn in Configure, for the tests of
// what Host and ConfigHost guarantee a plugin.
type hgPlugin struct {
	name        string
	initFn      func(ctx context.Context, h collage.Host) error
	configureFn func(ctx context.Context, h collage.ConfigHost) error
}

func (p *hgPlugin) Name() string {
	if p.name == "" {
		return "test/hg"
	}
	return p.name
}
func (p *hgPlugin) Version() string                { return "0" }
func (p *hgPlugin) Shutdown(context.Context) error { return nil }
func (p *hgPlugin) Init(ctx context.Context, h collage.Host) error {
	if p.initFn == nil {
		return nil
	}
	return p.initFn(ctx, h)
}
func (p *hgPlugin) Configure(ctx context.Context, h collage.ConfigHost) error {
	if p.configureFn == nil {
		return nil
	}
	return p.configureFn(ctx, h)
}

// A plugin writing into every container of its Page copy — CacheParams,
// FragmentPaths and its inner maps, Actions and each action's Methods,
// Redirects — reaches neither the page the router serves nor another
// plugin's copy (HOST-01).
func TestHost_PageCopySharesNoContainer(t *testing.T) {
	var fresh *collage.Page
	writer := &hgPlugin{name: "test/writer", initFn: func(_ context.Context, h collage.Host) error {
		page, _ := h.Page("search")
		page.CacheParams[0] = "unrelated"
		page.FragmentPaths["en"]["/injected"] = page.ContentFragment
		page.Actions[0].Methods[0] = http.MethodGet
		page.Redirects[0].To = "/hijacked"
		return nil
	}}
	reader := &hgPlugin{name: "test/reader", initFn: func(_ context.Context, h collage.Host) error {
		fresh, _ = h.Page("search")
		return nil
	}}
	live := collage.NewFragment("live", "live.html").Build()
	search := collage.NewPage("search").
		WithContent(collage.NewFragment("search", "search.html").WithData(collage.Load(
			func(_ context.Context, rc *collage.RenderContext) (string, error) {
				return rc.Request.URL.Query().Get("q"), nil
			})).Build()).
		WithPath("en", "/search").
		WithRedirect("/find", "/search", http.StatusMovedPermanently).
		WithFragmentPath("en", "/search/live", live).
		Incremental(time.Minute).WithCacheParams("q").
		WithAction(http.MethodPost, func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
			return collage.SeeOther("/"), nil
		}).Build()
	site := collagetest.NewSite(t,
		collagetest.WithPlugins(writer, reader),
		collagetest.WithConfig(func(c *collage.Config) {
			c.Cache = collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour}
		}),
		collagetest.WithTemplates(map[string]string{"search.html": `q={{.}}`, "live.html": `live`}),
		collagetest.WithRegister(search),
	)

	if !slices.Equal(fresh.CacheParams, []string{"q"}) {
		t.Errorf("another plugin's copy has CacheParams %v", fresh.CacheParams)
	}
	if n := len(fresh.FragmentPaths["en"]); n != 1 {
		t.Errorf("another plugin's copy has %d fragment paths, want 1", n)
	}
	if got := fresh.Actions[0].Methods; !slices.Equal(got, []string{http.MethodPost}) {
		t.Errorf("another plugin's copy has action methods %v", got)
	}
	if got := fresh.Redirects[0].To; got != "/search" {
		t.Errorf("another plugin's copy has redirect to %q", got)
	}
	site.Get("/search?q=alice").WantStatus(http.StatusOK)
	if body := site.Get("/search?q=bob").WantStatus(http.StatusOK).Body; body != "q=bob" {
		t.Errorf("GET /search?q=bob = %q: the cache key lost its parameter", body)
	}
	site.Get("/search/injected").WantStatus(http.StatusNotFound)
	if got := site.Get("/find").WantStatus(http.StatusMovedPermanently).Header.Get("Location"); got != "/search" {
		t.Errorf("redirect Location = %q", got)
	}
}
