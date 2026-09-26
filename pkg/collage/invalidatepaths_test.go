package collage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

type purger struct {
	mu    sync.Mutex
	paths [][]string
}

func (p *purger) Name() string                             { return "test/purger" }
func (p *purger) Version() string                          { return "0" }
func (p *purger) Init(context.Context, collage.Host) error { return nil }
func (p *purger) Shutdown(context.Context) error           { return nil }
func (p *purger) OnCacheInvalidate(_ context.Context, ev *collage.CacheInvalidateEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paths = append(p.paths, ev.Paths)
	return nil
}

// An invalidation says which URLs it dropped — what a CDN purge needs — and an
// entry can be invalidated by its path.
func TestInvalidation_NamesThePaths(t *testing.T) {
	p := &purger{}
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>{{.}}</p>`)}}, Root: "t"},
		Cache:    collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour},
		Locale:   collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
		Plugins:  []collage.Plugin{p},
	})
	if err != nil {
		t.Fatal(err)
	}
	post := collage.NewFragment("post", "p.html").WithDataHandler(func(_ context.Context, rc *collage.RenderContext) (any, []string, error) {
		return rc.Param("slug"), []string{"post:" + rc.Param("slug"), "posts"}, nil
	}).Static().Build()
	if err := app.RegisterPage(collage.NewPage("post").WithContent(post).WithPath("en", "/posts/{slug}").WithPath("tr", "/yazilar/{slug}").Build()); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()
	for _, path := range []string{"/posts/a", "/posts/b", "/tr/yazilar/a"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	ctx := context.Background()
	_ = app.InvalidateTags(ctx, "post:a")
	_ = app.InvalidateTags(ctx, "posts")
	_ = app.InvalidateTags(ctx, "posts") // nothing left
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/posts/b", nil))
	_ = app.InvalidateTags(ctx, collage.PathTag("/posts/b"))

	got := make([]string, 0, len(p.paths))
	for _, paths := range p.paths {
		got = append(got, strings.Join(paths, ","))
	}
	want := []string{"/posts/a,/tr/yazilar/a", "/posts/b", "", "/posts/b"}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Errorf("paths per invalidation = %q, want %q", got, want)
	}
}
