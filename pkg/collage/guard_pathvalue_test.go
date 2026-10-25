package collage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

// A guard reads the route's parameters with r.PathValue, in every locale and for
// a catch-all, and an action on the page reads them from rc.Request the same way.
func TestGuard_ReadsPathValues(t *testing.T) {
	var seen []string
	guard := func(_ context.Context, r *http.Request) (*collage.GuardDecision, error) {
		seen = append(seen, r.PathValue("slug")+"|"+r.PathValue("rest"))
		if r.PathValue("slug") == "theirs" {
			return &collage.GuardDecision{Status: http.StatusNotFound}, nil
		}
		return nil, nil
	}
	var acted string
	edit := collage.NewPage("edit").
		WithContent(collage.NewFragment("edit", "edit.html").WithGuard(guard).Build()).
		WithPath("en", "/recipes/{slug}/edit").WithPath("tr", "/tarifler/{slug}/duzenle").
		WithActionFor(collage.NewAction("save").WithMethods(http.MethodPost).WithoutCSRF().
			WithHandler(func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
				acted = rc.Request.PathValue("slug")
				return collage.SeeOther("/"), nil
			}).Build()).
		Build()
	files := collage.NewPage("files").
		WithContent(collage.NewFragment("files", "edit.html").WithGuard(guard).Build()).
		WithPath("en", "/files/{rest...}").Build()
	h := collagetest.NewSite(t,
		collagetest.WithConfig(func(c *collage.Config) {
			c.Locale = collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}}
		}),
		collagetest.WithTemplates(map[string]string{"edit.html": `edit`}),
		collagetest.WithoutHome(),
		collagetest.WithRegister(edit, files),
	).App.Handler()

	for target, want := range map[string]int{
		"/recipes/soup/edit":         http.StatusOK,
		"/tr/tarifler/corba/duzenle": http.StatusOK,
		"/recipes/theirs/edit":       http.StatusNotFound,
		"/files/a/b.txt":             http.StatusOK,
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != want {
			t.Errorf("GET %s = %d, want %d", target, w.Code, want)
		}
	}
	got := map[string]bool{}
	for _, s := range seen {
		got[s] = true
	}
	for _, want := range []string{"soup|", "corba|", "theirs|", "|a/b.txt"} {
		if !got[want] {
			t.Errorf("guard saw %q, missing %q", seen, want)
		}
	}

	r := httptest.NewRequest(http.MethodPost, "/recipes/soup/edit", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if acted != "soup" {
		t.Errorf("action read slug %q (status %d), want soup", acted, w.Code)
	}
}
