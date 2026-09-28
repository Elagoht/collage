package collage_test

import (
	"net/http"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// Next.js: test/e2e/repeated-slashes ("should handle double slashes correctly with
// encoded"). An encoded slash is not a separator, so it is a 404 — including when
// it sits next to a real one, or next to a dot segment.
func TestNextjs_AnEncodedSlashBesideAnotherIsStillA404(t *testing.T) {
	h := slashApp(t, false).Handler()
	for _, target := range []string{"/%2Fgoogle.com", "/%2Fgoogle.com?hello=1", "/blog/%2Fhello", "/blog/hello%2F%2E%2E%2Fabout", "/about%2F"} {
		rec := request(h, http.MethodGet, target)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d to %q, want 404", target, rec.Code, rec.Header().Get("Location"))
		}
	}
}

// Next.js: test/production/pages-dir/production/test/security.ts ("should handle
// encoded value in the pathname to query correctly"). A value captured from the
// path and placed in the destination's query is escaped for a query.
func TestNextjs_ARedirectEscapesAValueItPutsInTheQuery(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/page.html": {Data: []byte(`<p>about</p>`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	about := collage.NewPage("about").WithContent(collage.NewFragment("about", "page.html").Build()).
		WithPath("en", "/about").Static().
		WithRedirect("/redirect-query-test/{x}", "/about?foo={x}", http.StatusTemporaryRedirect).
		Build()
	if err := app.RegisterPage(about); err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]string{
		"/redirect-query-test/a%26admin%3D1": "/about?foo=a%26admin%3D1",
		"/redirect-query-test/a+b":           "/about?foo=a%2Bb",
	} {
		rec := request(app.Handler(), http.MethodGet, target)
		if got := rec.Header().Get("Location"); got != want {
			t.Errorf("GET %s redirects to %q, want %q", target, got, want)
		}
	}
}
