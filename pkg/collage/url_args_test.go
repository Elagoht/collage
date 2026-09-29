package collage_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

type storyID int64

func (id storyID) String() string { return "s-" + strings.Repeat("x", int(id)) }

type storyView struct {
	ID    int64
	Rank  uint8
	Code  storyID
	Score float64
}

// argsApp renders one template with a story's numbers in it, so each case can
// put a different {{pageURL}} or {{actionURL}} call in front of real data.
func argsApp(t *testing.T, tmpl string, logs *bytes.Buffer) (http.Handler, *collage.App) {
	t.Helper()
	cfg := &collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"t/layout.html": {Data: []byte(`<main>{{slot "content"}}</main>{{slot "aside"}}`)},
				"t/home.html":   {Data: []byte(tmpl)},
				"t/story.html":  {Data: []byte(`story`)},
				"t/aside.html":  {Data: []byte(`aside`)},
			},
			Root: "t",
		},
		Locale: collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
	}
	if logs != nil {
		cfg.Logger = slog.New(slog.NewTextHandler(logs, nil))
	}
	app, err := collage.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// home is the one under test. It sits in an optional slot, as a slot
	// resolver's fragment does, so its failure is contained.
	home := collage.NewFragment("home", "home.html").WithDataHandler(collage.Load(func(context.Context, *collage.RenderContext) (storyView, error) {
		return storyView{ID: 42, Rank: 7, Code: 3, Score: 1.5}, nil
	})).Build()
	layout := collage.NewFragment("layout", "layout.html").WithSlot("content", true, false).
		WithSlot("aside", false, false).WithSlotFragment("aside", home).Build()
	noop := func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) { return nil, nil }
	for _, p := range []*collage.Page{
		collage.NewPage("home").WithLayouts(layout).WithContent(collage.NewFragment("wrap", "aside.html").Build()).
			WithPath("en", "/").WithPath("tr", "/").Dynamic().Build(),
		collage.NewPage("story").WithLayouts(layout).WithContent(collage.NewFragment("story", "story.html").Build()).
			WithPath("en", "/stories/{id}").WithPath("tr", "/yazilar/{id}").
			WithAction(http.MethodPost, noop).Dynamic().Build(),
	} {
		if err := app.RegisterPage(p); err != nil {
			t.Fatalf("RegisterPage(%q): %v", p.Name, err)
		}
	}
	for _, a := range []*collage.Action{
		collage.NewAction("logout").WithPath("en", "/logout").WithPath("tr", "/cikis").WithMethods(http.MethodPost).WithHandler(noop).Build(),
		collage.NewAction("vote").WithPath("en", "/stories/{id}/vote").WithMethods(http.MethodPost).WithHandler(noop).Build(),
	} {
		if err := app.RegisterAction(a); err != nil {
			t.Fatalf("RegisterAction(%q): %v", a.Name, err)
		}
	}
	return app.Handler(), app
}

func TestPageURL_TakesIntegersAndStringers(t *testing.T) {
	h, _ := argsApp(t, `<a href="{{pageURL "story" "id" .ID}}">a</a>`+
		`<a href="{{pageURL "story" "id" .Rank}}">b</a>`+
		`<a href="{{pageURL "story" "id" .Code}}">c</a>`+
		`<a href="{{pageURL "story" "id" 9}}">d</a>`+
		`<a href="{{pageURLIn "tr" "story" "id" .ID}}">e</a>`, nil)
	got := body(t, h, "/")
	for _, want := range []string{`href="/stories/42"`, `href="/stories/7"`, `href="/stories/s-xxx"`, `href="/stories/9"`, `href="/tr/yazilar/42"`} {
		if !strings.Contains(got, want) {
			t.Errorf("body = %q, want %s", got, want)
		}
	}
}

func TestPageURL_RefusesAValueThatIsNoPathSegment(t *testing.T) {
	var logs bytes.Buffer
	h, _ := argsApp(t, `<a href="{{pageURL "story" "id" .Score}}">a</a>`, &logs)
	if got := body(t, h, "/"); strings.Contains(got, "href") {
		t.Fatalf("body = %q, want the fragment to fail", got)
	}
	if !strings.Contains(logs.String(), "float64") {
		t.Fatalf("logs = %q, want the refusal to name the type", logs.String())
	}
}

// A contained failure used to leave only an empty slot and a 200: nothing in the
// output outside dev mode, and nothing in the log.
func TestFailedFragment_IsLogged(t *testing.T) {
	var logs bytes.Buffer
	h, _ := argsApp(t, `{{pageURL "nope"}}`, &logs)
	body(t, h, "/")
	out := logs.String()
	for _, want := range []string{"level=WARN", "fragment failed", "page=home", "fragment=home", "nope"} {
		if !strings.Contains(out, want) {
			t.Errorf("logs = %q, want %s", out, want)
		}
	}
}

func TestActionURL(t *testing.T) {
	h, _ := argsApp(t, `<form action="{{actionURL "logout"}}"></form>`+
		`<form action="{{actionURL "vote" "id" .ID}}"></form>`+
		`<form action="{{actionURL "story:POST" "id" .ID}}"></form>`, nil)
	got := body(t, h, "/")
	for _, want := range []string{`action="/logout"`, `action="/stories/42/vote"`, `action="/stories/42"`} {
		if !strings.Contains(got, want) {
			t.Errorf("body = %q, want %s", got, want)
		}
	}
	// In Turkish: its own path where it has one, the default one where it has not.
	got = body(t, h, "/tr")
	for _, want := range []string{`action="/tr/cikis"`, `action="/stories/42/vote"`, `action="/tr/yazilar/42"`} {
		if !strings.Contains(got, want) {
			t.Errorf("tr body = %q, want %s", got, want)
		}
	}
}

func TestAppActionURL(t *testing.T) {
	_, app := argsApp(t, `x`, nil)
	if got, err := app.ActionURL("logout", "tr", nil); err != nil || got != "/tr/cikis" {
		t.Errorf(`ActionURL("logout", "tr") = %q, %v`, got, err)
	}
	if got, err := app.ActionURL("vote", "", map[string]string{"id": "5"}); err != nil || got != "/stories/5/vote" {
		t.Errorf(`ActionURL("vote") = %q, %v`, got, err)
	}
	if _, err := app.ActionURL("vote", "tr", map[string]string{"id": "5"}); !errors.Is(err, collage.ErrNoPathInLocale) {
		t.Errorf(`ActionURL("vote", "tr") err = %v, want ErrNoPathInLocale`, err)
	}
	if _, err := app.ActionURL("story", "", nil); !errors.Is(err, collage.ErrUnknownRoute) {
		t.Errorf(`ActionURL("story") err = %v, want ErrUnknownRoute: a page is not an action`, err)
	}
	if _, err := app.ActionURL("vote", "", nil); !errors.Is(err, collage.ErrRouteParams) {
		t.Errorf(`ActionURL("vote") without id err = %v, want ErrRouteParams`, err)
	}
	if _, err := app.ActionURL("logout", "fr", nil); !errors.Is(err, collage.ErrLocaleUnreachable) {
		t.Errorf(`ActionURL("logout", "fr") err = %v, want ErrLocaleUnreachable`, err)
	}
}
