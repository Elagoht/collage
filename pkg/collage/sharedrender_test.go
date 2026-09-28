package collage_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// what a render saw of its request, written into the page so the test reads it
// back from the body.
func requestSeen(rc *collage.RenderContext) string {
	r := rc.Request
	return fmt.Sprintf("cookie=[%s] auth=[%s] lang=[%s] query=[%s] remote=[%s] host=[%s]",
		r.Header.Get("Cookie"), r.Header.Get("Authorization"), r.Header.Get("Accept-Language"),
		r.URL.RawQuery, r.RemoteAddr, r.Host)
}

// A page rendered once and served to every reader is handed only what its cache
// key holds: the path, the query parameters the page named, the headers middleware
// declared with collage.Vary, the host. Not the first reader's cookie, credentials
// or address, which the render could otherwise have written into the copy every
// later reader gets. Next.js refuses a cached function that reads cookies or
// undeclared search params; here the render cannot see them to read.
func TestSharedRender_SeesOnlyWhatItsKeyHolds(t *testing.T) {
	app := nextjsApp(t, false)
	if err := app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = collage.Vary(r, "Accept-Language", "tr")
			next.ServeHTTP(w, r)
		})
	}); err != nil {
		t.Fatal(err)
	}
	seen := collage.NewInlineFragment("seen", `<p>{{.}}</p>`).
		WithDataHandler(collage.Load(func(_ context.Context, rc *collage.RenderContext) (string, error) {
			return requestSeen(rc), nil
		})).Build()
	for _, page := range []*collage.Page{
		collage.NewPage("inc").WithContent(seen).WithPath("en", "/inc").Incremental(time.Hour).WithCacheParams("page").Build(),
		collage.NewPage("live").WithContent(seen).WithPath("en", "/live").Dynamic().Build(),
	} {
		if err := app.RegisterPage(page); err != nil {
			t.Fatal(err)
		}
	}
	header := http.Header{
		"Cookie":          {"session=first-readers"},
		"Authorization":   {"Bearer first-readers"},
		"Accept-Language": {"tr-TR"},
	}
	h := app.Handler()

	cached := nextjsGet(h, "/inc?page=2&utm=first-readers", header).Body.String()
	if strings.Contains(cached, "first-readers") || strings.Contains(cached, "192.0.2.1") {
		t.Errorf("a shared render saw the reader's own request: %s", cached)
	}
	for _, want := range []string{"lang=[tr-TR]", "query=[page=2]", "host=[example.com]"} {
		if !strings.Contains(cached, want) {
			t.Errorf("a shared render lost %s, which its key holds: %s", want, cached)
		}
	}

	// A dynamic page is rendered for each reader, and sees all of the request.
	live := nextjsGet(h, "/live?utm=mine", header).Body.String()
	for _, want := range []string{"session=first-readers", "Bearer first-readers", "utm=mine"} {
		if !strings.Contains(live, want) {
			t.Errorf("a dynamic render lost %s: %s", want, live)
		}
	}
}

// The host is part of a shared render's key. A render that builds an absolute
// URL from r.Host — a canonical link, an og:url — would otherwise be poisoned by
// one request carrying another Host, and served so to everyone.
func TestSharedRender_TheHostIsPartOfTheKey(t *testing.T) {
	app := nextjsApp(t, false)
	var renders atomic.Int32
	canonical := collage.NewInlineFragment("canonical", `<link rel="canonical" href="https://{{.}}/">`).
		WithDataHandler(collage.Load(func(_ context.Context, rc *collage.RenderContext) (string, error) {
			renders.Add(1)
			return rc.Request.Host, nil
		})).Build()
	if err := app.RegisterPage(collage.NewPage("home").WithContent(canonical).WithPath("en", "/").Incremental(time.Hour).Build()); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()

	poison := httpRequest(h, "/", "evil.example")
	if !strings.Contains(poison, "evil.example") {
		t.Fatalf("the render did not see its own host: %s", poison)
	}
	if got := httpRequest(h, "/", "site.example"); strings.Contains(got, "evil.example") {
		t.Errorf("a render for another Host was served: %s", got)
	}
	if got := httpRequest(h, "/", "site.example"); !strings.Contains(got, "site.example") || renders.Load() != 2 {
		t.Errorf("renders = %d, body %s; want one per host, the second served from the cache", renders.Load(), got)
	}
}

func httpRequest(h http.Handler, target, host string) string {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Body.String()
}
