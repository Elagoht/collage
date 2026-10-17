package collage_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// postSite is three pages, /p/a, /p/b and /p/c, all tagged "posts", and a
// POST /save action that retitles one of them, invalidates "posts" and
// redirects to location. Each page's data handler is uncached and counts its
// renders, so a render is counted once per page however the data cache is set.
type postSite struct {
	app *collage.App

	mu       sync.Mutex
	titles   map[string]string
	renders  map[string]int
	delay    map[string]time.Duration
	failure  error
	gate     chan struct{}
	location string
	retitle  string
}

type postView struct{ Title string }

func newPostSite(t *testing.T, cacheConfig collage.CacheConfig) *postSite {
	t.Helper()
	site := &postSite{
		titles:  map[string]string{"a": "old a", "b": "old b", "c": "old c"},
		renders: map[string]int{},
		delay:   map[string]time.Duration{},
	}
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/post.html": {Data: []byte(`<h1>{{.Title}}</h1>`)},
		}, Root: "t"},
		Cache: cacheConfig,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, name := range []string{"a", "b", "c"} {
		post := collage.NewFragment("post-"+name, "post.html").WithData(collage.Load(
			func(ctx context.Context, _ *collage.RenderContext) (postView, error) {
				site.mu.Lock()
				site.renders[name]++
				delay, gate, failure, title := site.delay[name], site.gate, site.failure, site.titles[name]
				site.mu.Unlock()
				if gate != nil {
					select {
					case <-gate:
					case <-ctx.Done():
						return postView{}, ctx.Err()
					}
				}
				if delay > 0 {
					time.Sleep(delay)
				}
				if failure != nil {
					return postView{}, failure
				}
				return postView{Title: title}, nil
			})).Required().Build()
		page := collage.NewPage("p-"+name).WithContent(post).WithPath("en", "/p/"+name).
			Incremental(time.Hour).WithDependencyTags("posts").Build()
		if err := app.RegisterPage(page); err != nil {
			t.Fatalf("RegisterPage: %v", err)
		}
	}
	save := collage.NewAction("save").WithPath("en", "/save").WithMethods(http.MethodPost).WithoutCSRF().
		WithHandler(func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
			site.mu.Lock()
			defer site.mu.Unlock()
			if site.retitle != "" {
				site.titles[site.retitle] = "new " + site.retitle
			}
			return &collage.ActionResult{InvalidateTags: []string{"posts"}, Location: site.location}, nil
		}).Build()
	if err := app.RegisterAction(save); err != nil {
		t.Fatalf("RegisterAction: %v", err)
	}
	site.app = app
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	return site
}

// prime writes a, b and c, in that order: a is the oldest recipe and c the
// newest, which is the one a warm run takes first.
func (s *postSite) prime(t *testing.T) {
	t.Helper()
	for _, name := range []string{"a", "b", "c"} {
		if rec := s.get(name); rec.Code != http.StatusOK {
			t.Fatalf("priming /p/%s = %d", name, rec.Code)
		}
	}
}

func (s *postSite) get(name string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p/"+name, nil))
	return rec
}

func (s *postSite) post(header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/save", nil)
	for name, values := range header {
		req.Header[name] = values
	}
	rec := httptest.NewRecorder()
	s.app.Handler().ServeHTTP(rec, req)
	return rec
}

func (s *postSite) set(f func(s *postSite)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func (s *postSite) rendered(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renders[name]
}

func warmingCache() collage.CacheConfig {
	return collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour}
}

// The editor's redirected page view is a fresh HIT with what they just saved:
// the action warmed its target before it answered. The target is the oldest
// recipe and the background run's first page is slow, so the background run has
// not reached the target by the time the action answers.
func TestAction_RedirectTargetIsFreshOnFirstLoad(t *testing.T) {
	for _, tc := range []struct {
		name, location string
		fetch          bool
	}{
		{name: "path", location: "/p/a"},
		{name: "query and fragment", location: "/p/a?saved=1#top"},
		{name: "relative", location: "p/a"},
		{name: "absolute same host", location: "http://EXAMPLE.com/p/a"},
		{name: "fetch", location: "/p/a", fetch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			site := newPostSite(t, warmingCache())
			site.prime(t)
			site.set(func(s *postSite) {
				s.location, s.retitle = tc.location, "a"
				s.delay["c"] = 300 * time.Millisecond
			})
			header := http.Header{}
			if tc.fetch {
				header.Set("Collage-Fetch", "1")
			}
			rec := site.post(header)
			switch {
			case tc.fetch && (rec.Code != http.StatusNoContent || rec.Header().Get("Collage-Location") != tc.location):
				t.Fatalf("fetch POST = %d %q, want 204 with the location", rec.Code, rec.Header().Get("Collage-Location"))
			case !tc.fetch && (rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != tc.location):
				t.Fatalf("POST = %d %q, want 303 to the location", rec.Code, rec.Header().Get("Location"))
			}
			got := site.get("a")
			if status := got.Header().Get(collage.CacheStatusHeader); status != "HIT" || got.Body.String() != "<h1>new a</h1>" {
				t.Fatalf("redirect target = %s %q, want a HIT on the new title", status, got.Body.String())
			}
		})
	}
}

// With the upstream down the redirect still goes out, and the target is
// served stale, as without warming.
func TestAction_UpstreamDownStillRedirects(t *testing.T) {
	site := newPostSite(t, warmingCache())
	site.prime(t)
	site.set(func(s *postSite) {
		s.location = "/p/a"
		s.failure = errors.New("cms down")
	})
	began := time.Now()
	rec := site.post(nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/p/a" {
		t.Fatalf("POST = %d %q, want the redirect", rec.Code, rec.Header().Get("Location"))
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("the redirect took %v", took)
	}
	got := site.get("a")
	if status := got.Header().Get(collage.CacheStatusHeader); got.Code != http.StatusOK || status != "STALE" || got.Body.String() != "<h1>old a</h1>" {
		t.Fatalf("redirect target = %d %s %q, want the stale page", got.Code, status, got.Body.String())
	}
}

// The target is also the background run's first page. The action takes it
// from the queue, or joins the render already under way, and it is rendered
// once either way.
func TestAction_WarmsTheRedirectTargetOnce(t *testing.T) {
	site := newPostSite(t, warmingCache())
	site.prime(t)
	site.set(func(s *postSite) { s.location, s.retitle = "/p/c", "c" })
	if rec := site.post(nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d", rec.Code)
	}
	got := site.get("c")
	if status := got.Header().Get(collage.CacheStatusHeader); status != "HIT" || got.Body.String() != "<h1>new c</h1>" {
		t.Fatalf("redirect target = %s %q, want a HIT on the new title", status, got.Body.String())
	}
	waitWarmed(t, site.app)
	for _, name := range []string{"a", "b", "c"} {
		if n := site.rendered(name); n != 2 {
			t.Errorf("/p/%s rendered %d times, want once to prime and once to warm", name, n)
		}
	}
}

// A redirect to another origin is no page of this cache, and is not waited for:
// the action answers while the upstream holds every render.
func TestAction_CrossOriginLocationIsNotWarmed(t *testing.T) {
	for _, location := range []string{"https://other.example/p/a", "//other.example/p/a", "mailto:x@example.com"} {
		t.Run(location, func(t *testing.T) {
			site := newPostSite(t, warmingCache())
			site.prime(t)
			gate := make(chan struct{})
			// After the site's own cleanup is registered, so it runs first: a
			// shutdown would otherwise wait on the held render.
			t.Cleanup(func() { close(gate) })
			site.set(func(s *postSite) { s.location, s.gate = location, gate })

			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- site.post(nil) }()
			select {
			case rec := <-done:
				if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != location {
					t.Fatalf("POST = %d %q, want the redirect", rec.Code, rec.Header().Get("Location"))
				}
			case <-time.After(time.Second):
				t.Fatal("the action waited on a warm for another origin")
			}
			if n := site.rendered("a"); n != 1 {
				t.Fatalf("/p/a rendered %d times, want only its priming", n)
			}
		})
	}
}

// With warming off the action warms nothing either: the editor's request renders
// the target itself, as before warming.
func TestAction_WarmingOffDoesNotWarm(t *testing.T) {
	config := warmingCache()
	config.WarmLimit = -1
	site := newPostSite(t, config)
	site.prime(t)
	site.set(func(s *postSite) { s.location, s.retitle = "/p/a", "a" })
	if rec := site.post(nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d", rec.Code)
	}
	if got := site.get("a"); got.Header().Get(collage.CacheStatusHeader) != "MISS" {
		t.Fatalf("redirect target = %s, want a MISS with warming off", got.Header().Get(collage.CacheStatusHeader))
	}
}
