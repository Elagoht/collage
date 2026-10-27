package collage_test

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

var fkwCount = collage.NewKey[int]("fkw-count")

// fkwMissed is the part of the warning every test looks for.
const fkwMissed = "nothing in this render set"

// fkwPage is a page whose parent fragment Sets a key its child Gets, the child
// opened at a path of its own.
func fkwPage() *collage.Page {
	child := collage.NewFragment("count", "count.html").WithData(collage.Load(
		func(_ context.Context, rc *collage.RenderContext) (int, error) {
			n, _ := fkwCount.Get(rc)
			return n, nil
		})).Build()
	parent := collage.NewFragment("board", "board.html").WithData(collage.Load(
		func(_ context.Context, rc *collage.RenderContext) (string, error) {
			fkwCount.Set(rc, 7)
			return "board", nil
		})).WithSlotFragment("count", child).Build()
	return collage.NewPage("counter").WithContent(parent).WithPath("en", "/counter").
		WithFragmentPath("en", "/counter/count", child).Build()
}

func fkwSite(t *testing.T, dev bool) *collagetest.Site {
	t.Helper()
	return collagetest.NewSite(t,
		collagetest.WithTemplates(map[string]string{
			"board.html": `<div>{{.}} {{slot "count"}}</div>`,
			"count.html": `<p>n={{.}}</p>`,
		}),
		collagetest.WithRegister(fkwPage()),
		collagetest.WithConfig(func(c *collage.Config) { c.DevMode = dev }))
}

// Inside the full page the parent's Set is seen, and nothing is said.
func TestFragmentKeyWarn_FullPageIsSilent(t *testing.T) {
	site := fkwSite(t, true)
	res := site.Get("/counter").WantStatus(http.StatusOK)
	if !strings.Contains(res.Body, "n=7") {
		t.Fatalf("page body = %q, want the parent's value n=7", res.Body)
	}
	if site.Logs.Has(slog.LevelWarn, fkwMissed) {
		t.Errorf("a full-page render warned:\n%s", site.Logs)
	}
}

// At its own path the parent does not run: in development the miss is said,
// once however often the fragment is asked for.
func TestFragmentKeyWarn_FragmentPathWarnsOnceInDev(t *testing.T) {
	site := fkwSite(t, true)
	for range 2 {
		res := site.Get("/counter/count").WantStatus(http.StatusOK)
		if !strings.Contains(res.Body, "n=0") {
			t.Fatalf("fragment body = %q, want the zero value n=0", res.Body)
		}
	}
	if !site.Logs.Has(slog.LevelWarn, fkwMissed, "fkw-count", "page=counter", "fragment=count", "collage.Once") {
		t.Fatalf("no warning naming the key, page and fragment:\n%s", site.Logs)
	}
	if n := strings.Count(site.Logs.String(), fkwMissed); n != 1 {
		t.Errorf("warned %d times, want once:\n%s", n, site.Logs)
	}
}

// In production the miss costs nothing and says nothing.
func TestFragmentKeyWarn_FragmentPathSilentInProduction(t *testing.T) {
	site := fkwSite(t, false)
	res := site.Get("/counter/count").WantStatus(http.StatusOK)
	if !strings.Contains(res.Body, "n=0") {
		t.Fatalf("fragment body = %q, want the zero value n=0", res.Body)
	}
	if site.Logs.Has(slog.LevelWarn, fkwMissed) {
		t.Errorf("production warned:\n%s", site.Logs)
	}
}

// A pushed fragment — App.RenderFragment by Path — is the same render, and warns
// the same.
func TestFragmentKeyWarn_RenderFragmentWarnsInDev(t *testing.T) {
	site := fkwSite(t, true)
	got, err := site.App.RenderFragment(site.Request(http.MethodGet, "/", nil), collage.FragmentRequest{Path: "/counter/count"})
	if err != nil {
		t.Fatalf("RenderFragment: %v", err)
	}
	if !strings.Contains(string(got.HTML), "n=0") {
		t.Fatalf("RenderFragment HTML = %q, want n=0", got.HTML)
	}
	if !site.Logs.Has(slog.LevelWarn, fkwMissed, "fkw-count") {
		t.Errorf("RenderFragment did not warn:\n%s", site.Logs)
	}
}
