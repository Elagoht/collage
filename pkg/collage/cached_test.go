package collage_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

type author struct{ Name string }

// authorSite is thirty posts, twenty by A and ten by B, each showing an author
// card whose data handler fetches through Cached and counts what it fetched.
type authorSite struct {
	app   *collage.App
	mu    sync.Mutex
	calls map[string]int
	names map[string]string
}

func newAuthorSite(t *testing.T, devMode bool) *authorSite {
	t.Helper()
	site := &authorSite{calls: map[string]int{}, names: map[string]string{"A": "Ada", "B": "Bo"}}
	app, err := collage.New(&collage.Config{
		DevMode: devMode,
		Server:  collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/post.html":   {Data: []byte(`<article>{{slot "author"}}</article>`)},
			"t/author.html": {Data: []byte(`<p>{{.Name}}</p>`)},
		}, Root: "t"},
		Cache: collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	card := collage.NewFragment("author", "author.html").
		WithDataHandler(collage.DataHandler(func(_ context.Context, rc *collage.RenderContext) (author, []string, error) {
			id, _ := collage.Get[string](rc, "author")
			a, err := collage.Cached(rc, "author:"+id, time.Hour, []string{"author:" + id},
				func(context.Context) (author, error) {
					site.mu.Lock()
					defer site.mu.Unlock()
					site.calls[id]++
					return author{Name: site.names[id]}, nil
				})
			return a, nil, err
		})).Build()
	for i := range 30 {
		id := "A"
		if i >= 20 {
			id = "B"
		}
		post := collage.NewFragment(fmt.Sprintf("post-%d", i), "post.html").
			WithDataHandler(collage.Effect(func(_ context.Context, rc *collage.RenderContext) error { rc.Set("author", id); return nil })).
			WithSlot("author", true, false).WithSlotFragment("author", card).Build()
		page := collage.NewPage(fmt.Sprintf("p%d", i)).WithContent(post).
			WithPath("en", fmt.Sprintf("/p/%d", i)).Incremental(time.Hour).Build()
		if err := app.RegisterPage(page); err != nil {
			t.Fatalf("RegisterPage: %v", err)
		}
	}
	site.app = app
	return site
}

func (s *authorSite) fetched() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for k, v := range s.calls {
		out[k] = v
	}
	return out
}

func (s *authorSite) get(t *testing.T, i int, preview bool) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/p/%d", i), nil)
	if preview {
		req.Header.Set("X-Preview", "1")
	}
	rec := httptest.NewRecorder()
	s.app.Handler().ServeHTTP(rec, req)
	return rec.Body.String()
}

// Thirty pages by two authors ask for each author once — exported or served.
func TestCached_SharesAcrossPages(t *testing.T) {
	site := newAuthorSite(t, false)
	builder, err := collage.NewBuilder(site.app, collage.BuildOptions{OutDir: t.TempDir(), Concurrency: 8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := site.fetched(); got["A"] != 1 || got["B"] != 1 {
		t.Errorf("export fetched %v, want each author once", got)
	}

	served := newAuthorSite(t, false)
	for i := range 30 {
		served.get(t, i, false)
	}
	if got := served.fetched(); got["A"] != 1 || got["B"] != 1 {
		t.Errorf("serving fetched %v, want each author once", got)
	}
}

// One InvalidateTags replaces the author and every page showing them — the pages
// carry the tag without the handler returning it.
func TestCached_InvalidatesWithThePages(t *testing.T) {
	site := newAuthorSite(t, false)
	for i := range 30 {
		site.get(t, i, false)
	}
	site.mu.Lock()
	site.names["A"] = "Ada Lovelace"
	site.mu.Unlock()

	dropped, err := site.app.InvalidateTagsN(context.Background(), "author:A")
	if err != nil || dropped != 20 {
		t.Fatalf("InvalidateTagsN = %d, %v; want A's twenty pages dropped", dropped, err)
	}
	if body := site.get(t, 3, false); body != "<article><p>Ada Lovelace</p></article>" {
		t.Errorf("A's page after the invalidation = %q, want the new name", body)
	}
	if body := site.get(t, 25, false); body != "<article><p>Bo</p></article>" {
		t.Errorf("B's page = %q", body)
	}
	if got := site.fetched(); got["A"] != 2 || got["B"] != 1 {
		t.Errorf("fetched %v, want A twice and B once", got)
	}
}

// A preview fetches fresh and stores nothing, so an editor sees the change and
// nobody else is served it.
func TestCached_APreviewSkipsIt(t *testing.T) {
	site := newAuthorSite(t, false)
	if err := site.app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Preview") != "" {
				collage.SkipCache(r)
			}
			next.ServeHTTP(w, r)
		})
	}); err != nil {
		t.Fatal(err)
	}
	site.get(t, 0, false)
	site.mu.Lock()
	site.names["A"] = "Draft name"
	site.mu.Unlock()

	if body := site.get(t, 1, true); body != "<article><p>Draft name</p></article>" {
		t.Errorf("preview = %q, want the fresh value", body)
	}
	if body := site.get(t, 1, false); body != "<article><p>Ada</p></article>" {
		t.Errorf("reader after the preview = %q, want the stored value: a preview must not store what it fetched", body)
	}
}

// In development nothing outlives a render, as with the page cache.
func TestCached_DevelopmentKeepsNothing(t *testing.T) {
	site := newAuthorSite(t, true)
	for range 3 {
		site.get(t, 0, false)
	}
	if got := site.fetched(); got["A"] != 3 {
		t.Errorf("development fetched %v, want A once per render", got)
	}
}

// A document's handler shares Cached with the pages: a feed and the posts it lists
// fetch an author once between them.
func TestCached_InADocument(t *testing.T) {
	site := newAuthorSite(t, false)
	doc := collage.NewDocument("authors", "text/plain").WithPath("en", "/authors.txt").Dynamic().
		WithHandler(func(ctx context.Context, rc *collage.RenderContext) ([]byte, []string, error) {
			a, err := collage.Cached(rc, "author:A", time.Hour, []string{"author:A"}, func(context.Context) (author, error) {
				site.mu.Lock()
				defer site.mu.Unlock()
				site.calls["A"]++
				return author{Name: site.names["A"]}, nil
			})
			return []byte(a.Name), nil, err
		}).Build()
	if err := site.app.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	site.get(t, 0, false)
	for range 3 {
		rec := httptest.NewRecorder()
		site.app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/authors.txt", nil))
		if rec.Body.String() != "Ada" {
			t.Fatalf("document = %q", rec.Body.String())
		}
	}
	if got := site.fetched(); got["A"] != 1 {
		t.Errorf("fetched %v, want A once across the page and three document requests", got)
	}
}

// A method a document does not answer is a 405 in the document's own kind: plain
// text, not the site's HTML error page.
func TestDocument_A405IsPlainText(t *testing.T) {
	site := newAuthorSite(t, false)
	doc := collage.NewDocument("robots", "text/plain").WithPath("en", "/robots.txt").Dynamic().
		WithHandler(func(context.Context, *collage.RenderContext) ([]byte, []string, error) {
			return []byte("User-agent: *"), nil, nil
		}).Build()
	if err := site.app.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	site.app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/robots.txt", nil))
	if rec.Code != http.StatusMethodNotAllowed || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("POST /robots.txt = %d %q, want a plain-text 405", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// Concurrent misses on one document run its handler once, as a page's do.
func TestDocument_ConcurrentMissesAreCoalesced(t *testing.T) {
	var runs atomic.Int32
	release := make(chan struct{})
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/x.html": {Data: []byte(`x`)}}, Root: "t"},
		Cache:    collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc := collage.NewDocument("feed", "application/xml").WithPath("en", "/feed.xml").Incremental(time.Minute).
		WithHandler(func(context.Context, *collage.RenderContext) ([]byte, []string, error) {
			runs.Add(1)
			<-release
			return []byte("<rss/>"), nil, nil
		}).Build()
	if err := app.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/feed.xml", nil))
			if rec.Body.String() != "<rss/>" {
				t.Errorf("body = %q", rec.Body.String())
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if runs.Load() != 1 {
		t.Errorf("handler runs = %d, want 1 for ten concurrent misses", runs.Load())
	}
}
