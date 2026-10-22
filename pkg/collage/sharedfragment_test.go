package collage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// A render RenderFragment reports Shared is pushed to every reader of its URL,
// so it is made from a request with nothing of the reader's in it — as the page
// cache's one render is — and differs only by host. Made from the reader's own
// request, it carried the first reader's cookie to every other reader.
func TestRenderFragment_SharedSeesNoReader(t *testing.T) {
	app := originSite(t, false, nil, &hostOrigins{origins: map[string]string{
		"a.test": "https://tenant-a.example",
		"b.test": "https://tenant-b.example",
	}})
	frag := collage.NewFragment("live", "o.html").WithData(collage.LoadTagged(
		func(_ context.Context, rc *collage.RenderContext) (string, []string, error) {
			who := "anon"
			if c, err := rc.Request.Cookie("user"); err == nil {
				who = c.Value
			}
			return collage.BaseURL(rc) + " user=" + who, []string{"t"}, nil
		})).Build()
	if err := app.RegisterPage(collage.NewPage("live").WithContent(frag).WithPath("en", "/live").
		WithFragmentPath("en", "/live/o", frag).Incremental(time.Minute).Build()); err != nil {
		t.Fatal(err)
	}

	render := func(host, user string) *collage.FragmentRender {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "http://"+host+"/stream", nil)
		if user != "" {
			r.AddCookie(&http.Cookie{Name: "user", Value: user})
		}
		got, err := app.RenderFragment(r, collage.FragmentRequest{Path: "/live/o"})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	a, b := render("a.test", "alice"), render("b.test", "bob")
	if !a.Shared || !b.Shared {
		t.Fatalf("Shared = %v, %v; want both, for an Incremental page", a.Shared, b.Shared)
	}
	for _, c := range []struct {
		got  *collage.FragmentRender
		want string
	}{{a, "https://tenant-a.example user=anon"}, {b, "https://tenant-b.example user=anon"}} {
		if !strings.Contains(string(c.got.HTML), c.want) {
			t.Errorf("shared render %q; want %q, the host's origin and no reader", c.got.HTML, c.want)
		}
	}
}
