package collage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// A fragment declares its own stylesheet and head elements, from a template or from
// Go, and they land in the layout's head: escaped, keyed, and once.
func TestHead_HelpersAndStylesheets(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"t/layout.html":  {Data: []byte(`<head>{{hoist "head"}}</head><body>{{slot "content"}}</body>`)},
				"t/article.html": {Data: []byte(`{{stylesheet "/static/article.css"}}<p>{{.}}</p>`)},
			},
			Root: "t",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.Mount("/static/", fstest.MapFS{"article.css": {Data: []byte("p{margin:0}")}}); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	layout := collage.NewFragment("layout", "layout.html").WithSlot("content", true, false).Build()
	content := collage.NewFragment("article", "article.html").
		WithDataHandler(collage.DataHandler(func(_ context.Context, rc *collage.RenderContext) (string, []string, error) {
			rc.HoistTitle(`Tom & "Jerry" <3`)
			rc.HoistMeta("description", `a "quoted" summary`)
			rc.HoistProperty("og:title", "Tom & Jerry")
			rc.HoistLink("canonical", "/articles/tom?x=1&y=2")
			rc.HoistAlternate("tr", "/tr/makaleler/tom")
			rc.HoistAlternate("en", "/articles/tom")
			// Declared again from Go: one link, not two.
			if err := rc.HoistStylesheet("/static/article.css"); err != nil {
				return "", nil, err
			}
			href, err := rc.Asset("/static/article.css")
			return href, nil, err
		})).
		Build()
	page := collage.NewPage("article").WithLayout(layout).WithContent(content).WithPath("en", "/a").Dynamic().Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d:\n%s", rec.Code, body)
	}

	head := body[:strings.Index(body, "</head>")]
	for _, want := range []string{
		`<title>Tom &amp; &#34;Jerry&#34; &lt;3</title>`,
		`<meta name="description" content="a &#34;quoted&#34; summary">`,
		`<meta property="og:title" content="Tom &amp; Jerry">`,
		`<link rel="canonical" href="/articles/tom?x=1&amp;y=2">`,
		`<link rel="alternate" hreflang="tr" href="/tr/makaleler/tom">`,
		`<link rel="alternate" hreflang="en" href="/articles/tom">`,
	} {
		if !strings.Contains(head, want) {
			t.Errorf("head does not contain %s:\n%s", want, head)
		}
	}
	links := regexp.MustCompile(`<link rel="stylesheet" href="(/static/article\.[0-9a-f]+\.css)">`).FindAllStringSubmatch(head, -1)
	if len(links) != 1 {
		t.Fatalf("stylesheet links in head = %d, want exactly one content-addressed link:\n%s", len(links), head)
	}
	if !strings.Contains(body, "<p>"+links[0][1]+"</p>") {
		t.Errorf("rc.Asset did not return the same URL the link uses:\n%s", body)
	}
}

// The head comes out the same whichever sibling's handler finishes first: a key is
// placed at its earliest declaration in the tree's order, not in arrival order —
// which, with siblings running concurrently, is whichever goroutine won.
func TestHead_SiblingsDeclareInTreeOrderWhoeverFinishesFirst(t *testing.T) {
	render := func(delayA, delayB time.Duration) string {
		app, err := collage.New(&collage.Config{
			Server: collage.ServerConfig{Host: "localhost", Port: 3000},
			Template: collage.TemplateConfig{FS: fstest.MapFS{
				"t/layout.html": {Data: []byte(`<head>{{hoist "head"}}</head>{{slot "a"}}{{slot "b"}}`)},
				"t/part.html":   {Data: []byte(`.`)},
			}, Root: "t"},
		})
		if err != nil {
			t.Fatal(err)
		}
		part := func(name string, delay time.Duration) *collage.Fragment {
			return collage.NewFragment(name, "part.html").
				WithDataHandler(collage.Effect(func(_ context.Context, rc *collage.RenderContext) error {
					time.Sleep(delay)
					rc.HoistMeta(name, name)
					return nil
				})).Build()
		}
		root := collage.NewFragment("root", "layout.html").
			WithSlot("a", true, false).WithSlotFragment("a", part("A", delayA)).
			WithSlot("b", true, false).WithSlotFragment("b", part("B", delayB)).Build()
		if err := app.RegisterPage(collage.NewPage("p").WithContent(root).WithPath("en", "/").Dynamic().Build()); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		return rec.Body.String()
	}
	aFirst := render(0, 30*time.Millisecond)
	bFirst := render(30*time.Millisecond, 0)
	if aFirst != bFirst {
		t.Errorf("the head depends on which handler finished first:\n%s\n%s", aFirst, bFirst)
	}
	if !strings.Contains(aFirst, `<meta name="A" content="A"><meta name="B" content="B">`) {
		t.Errorf("head = %s, want A before B, the order the tree declares them", aFirst)
	}
}
