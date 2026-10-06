package collage_test

import (
	"context"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

func inspectedApp(t *testing.T) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/layout.html":   {Data: []byte(`{{slot "content"}}`)},
			"t/post.html":     {Data: []byte(`{{slot "comments"}}{{slot "aside"}}`)},
			"t/comments.html": {Data: []byte(`c`)},
		}, Root: "t", Funcs: template.FuncMap{"shout": strings.ToUpper}},
		Locale: collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	comments := collage.NewFragment("comments", "comments.html").Shared().WithData(collage.DataHandler(
		func(context.Context, *collage.RenderContext) (string, []string, error) { return "", nil, nil })).
		Build()
	post := collage.NewFragment("post", "post.html").WithSlotFragment("comments", comments).WithSlot("aside", false, true).Build()
	layout := collage.NewFragment("layout", "layout.html").Build()
	if err := app.RegisterPage(collage.NewPage("post").WithLayouts(layout).WithContent(post).
		WithPath("en", "/blog/{slug}").WithPath("tr", "/yazi/{slug}").
		WithFragmentPath("en", "/blog/{slug}/comments", comments).Build()); err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterDocument(collage.NewDocument("feed", "application/rss+xml").AtRoot("/feed.xml").WithBody([]byte("x")).Build()); err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterAction(collage.NewAction("subscribe").WithPath("en", "/subscribe").WithMethods(http.MethodPost).
		WithHandler(func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
			return collage.SeeOther("/"), nil
		}).Build()); err != nil {
		t.Fatal(err)
	}
	if err := app.Mount("/static/", fstest.MapFS{"app.css": {Data: []byte("a")}, "img/logo.svg": {Data: []byte("<svg/>")}}); err != nil {
		t.Fatal(err)
	}
	return app
}

// Inspect describes the application as an editor needs it: names, patterns,
// parameters, the slots each template fills, functions and files.
func TestInspect(t *testing.T) {
	in := inspectedApp(t).Inspect()
	if in.Version != 1 || in.TemplateRoot != "t" || in.DefaultLocale != "en" || strings.Join(in.Locales, ",") != "en,tr" {
		t.Errorf("header = %+v", in)
	}
	if len(in.Pages) != 1 {
		t.Fatalf("pages = %+v", in.Pages)
	}
	p := in.Pages[0]
	if p.Name != "post" || p.Paths["tr"] != "/yazi/{slug}" || strings.Join(p.Params, ",") != "slug" || strings.Join(p.Layouts, ",") != "layout" || len(p.Guards) != 0 || p.Content != "post" {
		t.Errorf("page = %+v", p)
	}
	if len(p.FragmentPaths) != 1 || p.FragmentPaths[0].Fragment != "comments" || p.FragmentPaths[0].Pattern != "/blog/{slug}/comments" {
		t.Errorf("fragment paths = %+v", p.FragmentPaths)
	}
	frags := map[string]collage.InspectedFragment{}
	for _, f := range in.Fragments {
		frags[f.Name] = f
	}
	if f := frags["post"]; f.Template != "post.html" || strings.Join(f.Slots, ",") != "aside,comments" {
		t.Errorf("post fragment = %+v", f)
	}
	if f := frags["comments"]; !f.Handler || !f.Shared {
		t.Errorf("comments fragment = %+v", f)
	}
	if len(in.Fragments) != 3 {
		t.Errorf("fragments = %+v, want each once", in.Fragments)
	}
	if len(in.Documents) != 1 || in.Documents[0].Name != "feed" || in.Documents[0].ContentType != "application/rss+xml" {
		t.Errorf("documents = %+v", in.Documents)
	}
	if len(in.Actions) != 1 || in.Actions[0].Name != "subscribe" {
		t.Errorf("actions = %+v", in.Actions)
	}
	for _, fn := range []string{"slot", "pageURL", "shout"} {
		if !slices.Contains(in.TemplateFuncs, fn) {
			t.Errorf("template funcs lack %s: %v", fn, in.TemplateFuncs)
		}
	}
	if len(in.Mounts) != 1 || strings.Join(in.Mounts[0].Files, ",") != "/static/app.css,/static/img/logo.svg" {
		t.Errorf("mounts = %+v", in.Mounts)
	}
}

// `go run . collage-inspect` prints the same as JSON.
func TestDispatchCommands_Inspect(t *testing.T) {
	app := inspectedApp(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	code, err := collage.DispatchCommands(context.Background(), app, []string{collage.InspectCommand})
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)
	if code != 0 || err != nil {
		t.Fatalf("DispatchCommands = %d, %v", code, err)
	}
	var in collage.Inspection
	if err := json.Unmarshal(out, &in); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(in.Pages) != 1 || in.Pages[0].Name != "post" {
		t.Errorf("decoded = %+v", in)
	}
	if code, err := collage.DispatchCommands(context.Background(), inspectedApp(t), []string{collage.InspectCommand, "extra"}); code != 2 || err == nil {
		t.Errorf("with an argument: %d, %v", code, err)
	}
}

// A chained, guarded page reports its layouts outermost first and the spine
// fragments that carry a guard, in the order they are asked.
func TestInspectLayoutsAndGuards(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/layout.html": {Data: []byte(`{{slot "content"}}`)},
			"t/panel.html":  {Data: []byte(`p`)},
		}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	allow := func(context.Context, *http.Request) (*collage.GuardDecision, error) { return nil, nil }
	private := collage.NewFragment("private", "layout.html").WithGuard(allow).Build()
	auth := collage.NewFragment("auth", "layout.html").Build()
	panel := collage.NewFragment("panel", "panel.html").WithGuard(allow).Build()
	if err := app.RegisterPage(collage.NewPage("panel").WithLayouts(private, auth).WithContent(panel).
		WithPath("en", "/panel").Build()); err != nil {
		t.Fatal(err)
	}
	p := app.Inspect().Pages[0]
	if got := strings.Join(p.Layouts, ","); got != "private,auth" {
		t.Errorf("Layouts = %q, want private,auth", got)
	}
	if got := strings.Join(p.Guards, ","); got != "private,panel" {
		t.Errorf("Guards = %q, want private,panel", got)
	}
}

// A layout shared by two pages is one fragment the author wrote, reported once
// — not once per page's private copy of its slot table.
func TestInspectSharedLayoutOnce(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/layout.html": {Data: []byte(`{{slot "content"}}`)},
			"t/page.html":   {Data: []byte(`p`)},
		}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	master := collage.NewFragment("master", "layout.html").Build()
	auth := collage.NewFragment("auth", "layout.html").Build()
	for _, name := range []string{"a", "b"} {
		if err := app.RegisterPage(collage.NewPage(name).WithLayouts(master, auth).
			WithContent(collage.NewFragment(name, "page.html").Build()).WithPath("en", "/"+name).Build()); err != nil {
			t.Fatal(err)
		}
	}
	count := map[string]int{}
	for _, f := range app.Inspect().Fragments {
		count[f.Name]++
	}
	if count["master"] != 1 || count["auth"] != 1 {
		t.Errorf("fragment counts = %v, want master and auth once each", count)
	}
}

func TestInspectDataType(t *testing.T) {
	got := map[string]*string{}
	for _, f := range inspectedApp(t).Inspect().Fragments {
		got[f.Name] = f.DataType
	}
	if p := got["post"]; p == nil || *p != "nil" {
		t.Errorf("post (no data) dataType = %v, want \"nil\"", p)
	}
	if c := got["comments"]; c == nil || *c != "string" {
		t.Errorf("comments dataType = %v, want string", c)
	}
}
