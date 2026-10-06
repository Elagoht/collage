package collage_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// development/dev-cache-control: "sends no-store for an app router document".
func TestNextjs_DevelopmentPagesAreNoStore(t *testing.T) {
	app := nextjsApp(t, true)
	body := collage.NewInlineFragment("b", `<p>v</p>`).Build()
	for _, page := range []*collage.Page{
		collage.NewPage("inc").WithContent(body).WithPath("en", "/inc").Incremental(time.Hour).Build(),
		collage.NewPage("static").WithContent(body).WithPath("en", "/static").Static().Build(),
	} {
		if err := app.RegisterPage(page); err != nil {
			t.Fatal(err)
		}
	}
	doc := collage.NewDocument("robots", "text/plain").AtRoot("/robots.txt").WithBody([]byte("ok")).Build()
	if err := app.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()
	for _, path := range []string{"/inc", "/static", "/robots.txt"} {
		if cc := nextjsGet(h, path, nil).Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("dev %s Cache-Control = %q, want no-store", path, cc)
		}
	}
}

// A degraded render is never written to the server's cache; it must not be
// handed to every shared cache in front of it either.
func TestNextjs_ADegradedRenderIsNotPublic(t *testing.T) {
	app := nextjsApp(t, false)
	broken := collage.NewInlineFragment("broken", `<p>never</p>`).WithData(collage.Effect(
		func(context.Context, *collage.RenderContext) error {
			return errors.New("upstream down")
		})).
		Build()
	host := collage.NewInlineFragment("host", `<main>ok</main>{{slot "side"}}`).WithSlotFragment("side", broken).Build()
	if err := app.RegisterPage(collage.NewPage("p").WithContent(host).WithPath("en", "/p").Incremental(time.Hour).Build()); err != nil {
		t.Fatal(err)
	}
	rec := nextjsGet(app.Handler(), "/p", nil)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); strings.HasPrefix(cc, "public") {
		t.Errorf("degraded render Cache-Control = %q, want nothing a shared cache keeps", cc)
	}
}

// e2e/app-dir/segment-cache/cdn-cache-busting / e2e/vary-header: a response
// shaped by a request header names it in Vary.
func TestNextjs_FetchShapedRedirectVaries(t *testing.T) {
	app := nextjsApp(t, false)
	action := collage.NewAction("go").WithPath("en", "/go").WithMethods(http.MethodGet).
		WithHandler(func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
			res := collage.SeeOther("/elsewhere")
			res.Header = http.Header{"Cache-Control": {"public, max-age=60"}}
			return res, nil
		}).Build()
	if err := app.RegisterAction(action); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()
	plain := nextjsGet(h, "/go", nil)
	fetched := nextjsGet(h, "/go", http.Header{"Collage-Fetch": {"1"}})
	t.Logf("plain %d %v; fetch %d %v", plain.Code, plain.Header(), fetched.Code, fetched.Header())
	if !strings.Contains(fetched.Header().Get("Vary"), "Collage-Fetch") {
		t.Errorf("a response shaped by Collage-Fetch, publicly cacheable, has Vary %q", fetched.Header().Get("Vary"))
	}
}
