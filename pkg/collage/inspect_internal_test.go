package collage

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"html/template"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/internal/core"
)

func inspectedAppInternal(t *testing.T) *App {
	t.Helper()
	app, err := New(&Config{
		Server: ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{FS: fstest.MapFS{
			"t/layout.html":   {Data: []byte(`{{slot "content"}}`)},
			"t/post.html":     {Data: []byte(`{{slot "comments"}}{{slot "aside"}}`)},
			"t/comments.html": {Data: []byte(`c`)},
		}, Root: "t", Funcs: template.FuncMap{"shout": strings.ToUpper}},
		Locale: LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	comments := NewFragment("comments", "comments.html").Shared().WithData(LoadTagged(
		func(context.Context, *RenderContext) (string, []string, error) { return "", nil, nil })).
		Build()
	post := NewFragment("post", "post.html").WithSlotFragment("comments", comments).WithSlot("aside").Build()
	layout := NewFragment("layout", "layout.html").Build()
	if err := app.RegisterPage(NewPage("post").WithLayouts(layout).WithContent(post).
		WithPath("en", "/blog/{slug}").WithPath("tr", "/yazi/{slug}").
		WithFragmentPath("en", "/blog/{slug}/comments", comments).Build()); err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterDocument(NewDocument("feed", "application/rss+xml").AtRoot("/feed.xml").WithBody([]byte("x")).Build()); err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterAction(NewAction("subscribe").WithPath("en", "/subscribe").WithMethods(http.MethodPost).
		WithHandler(func(context.Context, *RenderContext) (*ActionResult, error) {
			return SeeOther("/"), nil
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
	in := inspectedAppInternal(t).c.Inspect()
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
	frags := map[string]core.InspectedFragment{}
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
	app := inspectedAppInternal(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	code, err := DispatchCommands(context.Background(), app, []string{inspectCommandName()})
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)
	if code != 0 || err != nil {
		t.Fatalf("DispatchCommands = %d, %v", code, err)
	}
	var in core.Inspection
	if err := json.Unmarshal(out, &in); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(in.Pages) != 1 || in.Pages[0].Name != "post" {
		t.Errorf("decoded = %+v", in)
	}
	if code, err := DispatchCommands(context.Background(), inspectedAppInternal(t), []string{inspectCommandName(), "extra"}); code != 2 || err == nil {
		t.Errorf("with an argument: %d, %v", code, err)
	}
}

// A chained, guarded page reports its layouts outermost first and the spine
// fragments that carry a guard, in the order they are asked.
func TestInspectLayoutsAndGuards(t *testing.T) {
	app, err := New(&Config{
		Server: ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{FS: fstest.MapFS{
			"t/layout.html": {Data: []byte(`{{slot "content"}}`)},
			"t/panel.html":  {Data: []byte(`p`)},
		}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	allow := func(context.Context, *http.Request) (*GuardDecision, error) { return nil, nil }
	private := NewFragment("private", "layout.html").WithGuard(allow).Build()
	auth := NewFragment("auth", "layout.html").Build()
	panel := NewFragment("panel", "panel.html").WithGuard(allow).Build()
	if err := app.RegisterPage(NewPage("panel").WithLayouts(private, auth).WithContent(panel).
		WithPath("en", "/panel").Build()); err != nil {
		t.Fatal(err)
	}
	p := app.c.Inspect().Pages[0]
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
	app, err := New(&Config{
		Server: ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{FS: fstest.MapFS{
			"t/layout.html": {Data: []byte(`{{slot "content"}}`)},
			"t/page.html":   {Data: []byte(`p`)},
		}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	master := NewFragment("master", "layout.html").Build()
	auth := NewFragment("auth", "layout.html").Build()
	for _, name := range []string{"a", "b"} {
		if err := app.RegisterPage(NewPage(name).WithLayouts(master, auth).
			WithContent(NewFragment(name, "page.html").Build()).WithPath("en", "/"+name).Build()); err != nil {
			t.Fatal(err)
		}
	}
	count := map[string]int{}
	for _, f := range app.c.Inspect().Fragments {
		count[f.Name]++
	}
	if count["master"] != 1 || count["auth"] != 1 {
		t.Errorf("fragment counts = %v, want master and auth once each", count)
	}
}

func TestInspectDataType(t *testing.T) {
	got := map[string]*string{}
	for _, f := range inspectedAppInternal(t).c.Inspect().Fragments {
		got[f.Name] = f.DataType
	}
	if p := got["post"]; p == nil || *p != "nil" {
		t.Errorf("post (no data) dataType = %v, want \"nil\"", p)
	}
	if c := got["comments"]; c == nil || *c != "string" {
		t.Errorf("comments dataType = %v, want string", c)
	}
}

type InspectBase struct{ ID int }
type inspectFriends []inspectUser
type inspectIndex map[string]*inspectUser
type inspectUser struct {
	InspectBase
	Name    string
	Friends inspectFriends
	Index   inspectIndex
}

func TestInspectJSONDataTypes(t *testing.T) {
	app, err := New(&Config{
		Server: ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{FS: fstest.MapFS{
			"t/a.html": {Data: []byte(`{{slot "b"}}{{slot "c"}}`)},
			"t/b.html": {Data: []byte(`x`)},
			"t/c.html": {Data: []byte(`{{.}}`)},
		}, Root: "t"},
		Locale: LocaleConfig{Default: "en", Supported: []string{"en"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := NewFragment("b", "b.html").WithoutTypeCheck().WithData(Load(
		func(context.Context, *RenderContext) (any, error) { return nil, nil })).Build() // any: data of unknown type, which inspect writes as null
	c := NewFragment("c", "c.html").WithData(Value(inspectUser{})).Build()
	a := NewFragment("a", "a.html").WithSlotFragment("b", b).WithSlotFragment("c", c).Build()
	if err := app.RegisterPage(NewPage("p").WithContent(a).WithPath("en", "/").Build()); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(app.c.Inspect())
	if err != nil {
		t.Fatal(err)
	}
	js := string(raw)
	for _, want := range []string{`"dataType":null`, `"dataType":"nil"`, `"typeCheck":false`, `"dataType":"collage.inspectUser"`, `"types":{`, `"collage.inspectUser":{"kind":"struct"`,
		`{"name":"InspectBase","type":"collage.InspectBase","embedded":true}`, `{"name":"ID","type":"int"}`,
		`"collage.inspectFriends":{"kind":"slice","elem":"collage.inspectUser"}`,
		`"collage.inspectIndex":{"kind":"map","key":"string","elem":"*collage.inspectUser"}`} {
		if !strings.Contains(js, want) {
			t.Errorf("inspect JSON lacks %s:\n%s", want, js)
		}
	}
}

var updateGolden = flag.Bool("update", false, "rewrite testdata golden files")

func inspectCommandName() string { return inspectCommand }

func captureStdout(t *testing.T, fn func()) []byte {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	was := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() { b, _ := io.ReadAll(r); done <- b }()
	fn()
	w.Close()
	os.Stdout = was
	return <-done
}

func TestInspect_GoldenJSON(t *testing.T) {
	app := inspectedAppInternal(t)
	out := captureStdout(t, func() {
		if code, err := DispatchCommands(context.Background(), app, []string{inspectCommandName()}); err != nil || code != 0 {
			t.Errorf("DispatchCommands = %d, %v", code, err)
		}
	})
	if *updateGolden {
		if err := os.WriteFile("testdata/inspect.golden.json", out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/inspect.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, want) {
		t.Errorf("collage inspect output changed:\n%s", out)
	}
}

// An inline fragment is inspected as inline, with no template path.
func TestInspect_InlineFragment(t *testing.T) {
	app, err := New(&Config{
		Server:   ServerConfig{Host: "localhost", Port: 0},
		Template: TemplateConfig{FS: fstest.MapFS{"templates/layouts/default.html": {Data: []byte(`{{slot "content"}}`)}}, Root: "templates"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.RegisterPage(NewPage("p").WithContent(NewInlineFragment("card", `<p/>`).Build()).
		WithPath("en", "/p").Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	for _, f := range app.c.Inspect().Fragments {
		if f.Name == "card" {
			if !f.Inline || f.Template != "" {
				t.Fatalf("inspected = %+v, want inline and no template path", f)
			}
			return
		}
	}
	t.Fatal("inline fragment not inspected")
}
