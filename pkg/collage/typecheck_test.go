package collage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

type tcAuthor struct{ Name string }
type tcPost struct {
	Title  string
	Author tcAuthor
}

func tcApp(t *testing.T, files fstest.MapFS) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 0},
		Template: collage.TemplateConfig{FS: files, Root: "t"},
		Cache:    collage.CacheConfig{Enabled: false, DefaultTTL: time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func loadPost(context.Context, *collage.RenderContext) (tcPost, error) {
	return tcPost{Title: "t"}, nil
}

func TestRegister_TemplateTypeErrors(t *testing.T) {
	files := fstest.MapFS{
		"t/layout.html":  {Data: []byte(`<html>{{.Whatever}}{{range .}}{{end}}{{template "partial.html"}}{{slot "content"}}</html>`)},
		"t/partial.html": {Data: []byte(`{{.Anything}}`)},
		"t/post.html":    {Data: []byte("{{.Titel}}\n{{.Author.Nmae}}\n{{.Title}}")},
	}
	app := tcApp(t, files)
	layout := collage.NewFragment("layout", "layout.html").Build()
	content := collage.NewFragment("post-body", "post.html").WithData(collage.Load(loadPost)).Build()
	err := app.RegisterPage(collage.NewPage("post").WithLayouts(layout).WithContent(content).WithPath("en", "/").Build())
	if !errors.Is(err, collage.ErrTemplateType) {
		t.Fatalf("RegisterPage error = %v, want ErrTemplateType", err)
	}
	var typeErrs []*collage.TemplateTypeError
	for _, e := range unwrapAll(err) {
		var te *collage.TemplateTypeError
		if errors.As(e, &te) {
			typeErrs = append(typeErrs, te)
		}
	}
	if len(typeErrs) != 2 {
		t.Fatalf("findings = %d (%v), want 2: the data-less layout reports nothing", len(typeErrs), err)
	}
	first, second := typeErrs[0], typeErrs[1]
	if first.Page != "post" || first.Fragment != "post-body" || first.Template != "post.html" || first.Line != 1 || first.Suggestion != "Title" {
		t.Errorf("first = %+v", first)
	}
	if second.Line != 2 || !strings.Contains(second.Reason, "Nmae") {
		t.Errorf("second = %+v", second)
	}
}

// A body behind a literal false condition never runs, declaration or not, so
// what it reads is not judged: html/template renders the page fine.
func TestRegister_DeclaredLiteralConditionSkipsDeadBody(t *testing.T) {
	files := fstest.MapFS{
		"t/post.html": {Data: []byte(`{{if $x := false}}{{.Nope}}{{end}}{{.Title}}`)},
	}
	app := tcApp(t, files)
	content := collage.NewFragment("post-body", "post.html").WithData(collage.Load(loadPost)).Build()
	if err := app.RegisterPage(collage.NewPage("post").WithContent(content).WithPath("en", "/").Build()); err != nil {
		t.Fatalf("RegisterPage error = %v, want nil", err)
	}
}

func unwrapAll(err error) []error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, e := range joined.Unwrap() {
			out = append(out, unwrapAll(e)...)
		}
		return out
	}
	return []error{err}
}

func TestRegister_TemplateTypeCheckOptOuts(t *testing.T) {
	files := fstest.MapFS{"t/post.html": {Data: []byte(`{{.Titel}}`)}}
	tests := []struct {
		name string
		f    *collage.Fragment
	}{
		{"WithoutTypeCheck", collage.NewFragment("p", "post.html").WithData(collage.Load(loadPost)).WithoutTypeCheck().Build()},
		{"Load[any]", collage.NewFragment("p", "post.html").WithData(collage.Load(func(context.Context, *collage.RenderContext) (any, error) { // any: the explicit opt-out under test
			return tcPost{}, nil
		})).Build()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := tcApp(t, files)
			if err := app.RegisterPage(collage.NewPage("p").WithContent(test.f).WithPath("en", "/").Build()); err != nil {
				t.Errorf("RegisterPage = %v, want nil", err)
			}
		})
	}
}

func TestRegister_TemplateTypeCheckSeesInlineAndFallback(t *testing.T) {
	app := tcApp(t, fstest.MapFS{"t/keep.html": {Data: []byte(`x`)}})
	fallback := collage.NewInlineFragment("fb", `{{.Nope}}`).WithData(collage.Value(tcAuthor{})).Build()
	content := collage.NewInlineFragment("c", `{{.Titel}}`).WithData(collage.Load(loadPost)).WithFallback(fallback).Build()
	err := app.RegisterPage(collage.NewPage("p").WithContent(content).WithPath("en", "/").Build())
	if err == nil || !strings.Contains(err.Error(), `inline template of fragment "c"`) || !strings.Contains(err.Error(), "Nope") {
		t.Errorf("RegisterPage = %v, want findings in the inline content and its fallback", err)
	}
}

func TestRegister_SharedPartialNamesTheIncludingFragment(t *testing.T) {
	files := fstest.MapFS{
		"t/card.html": {Data: []byte(`{{.Name}}`)},
		"t/a.html":    {Data: []byte(`{{template "card.html" .Author}}`)},
		"t/b.html":    {Data: []byte(`{{template "card.html" .}}`)},
	}
	app := tcApp(t, files)
	a := collage.NewFragment("a", "a.html").WithData(collage.Load(loadPost)).Build()
	b := collage.NewFragment("b", "b.html").WithData(collage.Load(loadPost)).Build()
	if err := app.RegisterPage(collage.NewPage("pa").WithContent(a).WithPath("en", "/a").Build()); err != nil {
		t.Errorf("page a = %v, want nil: the partial fits tcAuthor", err)
	}
	err := app.RegisterPage(collage.NewPage("pb").WithContent(b).WithPath("en", "/b").Build())
	var te *collage.TemplateTypeError
	if !errors.As(err, &te) || te.Fragment != "b" || te.Template != "card.html" {
		t.Errorf("page b = %v, want a finding in card.html naming fragment b", err)
	}
}

func TestRegister_TemplateTypeErrorsInAFixedOrder(t *testing.T) {
	files := fstest.MapFS{
		"t/layout.html": {Data: []byte(`{{slot "a"}}{{slot "b"}}`)},
		"t/a.html":      {Data: []byte(`{{.Zz}}`)},
		"t/b.html":      {Data: []byte(`{{.Aa}}`)},
	}
	for run := range 20 {
		app := tcApp(t, files)
		layout := collage.NewFragment("layout", "layout.html").
			WithSlotFragment("b", collage.NewFragment("b", "b.html").WithData(collage.Load(loadPost)).Build()).
			WithSlotFragment("a", collage.NewFragment("a", "a.html").WithData(collage.Load(loadPost)).Build()).
			Build()
		err := app.RegisterPage(collage.NewPage("p").WithContent(layout).WithPath("en", "/").Build())
		var order []string
		for _, e := range unwrapAll(err) {
			var te *collage.TemplateTypeError
			if errors.As(e, &te) {
				order = append(order, te.Template)
			}
		}
		if strings.Join(order, ",") != "a.html,b.html" {
			t.Fatalf("run %d: order = %v, want a.html then b.html", run, order)
		}
	}
}

// A template calling a slot by a name it works out as it renders skips the
// unknown-slot check; it must not skip the type check with it.
func TestRegister_TemplateTypeCheckSeesDynamicSlotCallers(t *testing.T) {
	app := tcApp(t, fstest.MapFS{"t/post.html": {Data: []byte(`{{slot .Title}}{{.Titel}}`)}})
	f := collage.NewFragment("p", "post.html").WithData(collage.Load(loadPost)).Build()
	err := app.RegisterPage(collage.NewPage("p").WithContent(f).WithPath("en", "/").Build())
	var te *collage.TemplateTypeError
	if !errors.As(err, &te) || te.Suggestion != "Title" {
		t.Errorf("RegisterPage = %v, want the finding for {{.Titel}}", err)
	}
}

// A fragment with no data, or an Effect, has no dot type, yet its function calls
// are still checked: a wrong argument count fails at registration.
func TestRegister_DataLessFragmentsStillCheckCalls(t *testing.T) {
	files := fstest.MapFS{
		"t/layout.html": {Data: []byte(`<main>{{asset}}{{slot "content"}}</main>`)},
		"t/effect.html": {Data: []byte(`{{slot "a" "b"}}`)},
	}
	app := tcApp(t, files)
	layout := collage.NewFragment("layout", "layout.html").Build()
	effect := collage.NewFragment("effect", "effect.html").WithData(collage.Effect(func(context.Context, *collage.RenderContext) error {
		return nil
	})).Build()
	err := app.RegisterPage(collage.NewPage("p").WithLayouts(layout).WithContent(effect).WithPath("en", "/").Build())
	got := map[string]int{}
	for _, e := range unwrapAll(err) {
		var te *collage.TemplateTypeError
		if errors.As(e, &te) {
			got[te.Fragment]++
		}
	}
	if got["layout"] != 1 || got["effect"] != 1 || len(got) != 2 {
		t.Errorf("findings by fragment = %v (%v), want one each for layout and effect", got, err)
	}
}

// Fragments reached only through a fragment path, a page's not-found page or its
// error page are checked too, and their findings name that page and fragment.
func TestRegister_TemplateTypeCheckSeesEveryReachableFragment(t *testing.T) {
	files := fstest.MapFS{
		"t/ok.html":  {Data: []byte(`ok`)},
		"t/bad.html": {Data: []byte(`{{.Titel}}`)},
	}
	bad := func() *collage.Fragment {
		return collage.NewFragment("bad", "bad.html").WithData(collage.Load(loadPost)).Build()
	}
	ok := func() *collage.Fragment { return collage.NewFragment("ok", "ok.html").Build() }
	tests := []struct {
		name, page string
		build      func() *collage.Page
	}{
		{"fragment path", "p", func() *collage.Page {
			return collage.NewPage("p").WithContent(ok()).WithPath("en", "/").WithFragmentPath("en", "/bad", bad()).Build()
		}},
		{"not-found page", "missing", func() *collage.Page {
			return collage.NewPage("p").WithContent(ok()).WithPath("en", "/").
				WithNotFoundPage(collage.NewPage("missing").WithContent(bad()).Build()).Build()
		}},
		{"error page", "oops", func() *collage.Page {
			return collage.NewPage("p").WithContent(ok()).WithPath("en", "/").
				WithErrorPage(collage.NewPage("oops").WithContent(bad()).Build()).Build()
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := tcApp(t, files)
			err := app.RegisterPage(test.build())
			var te *collage.TemplateTypeError
			if !errors.As(err, &te) || te.Page != test.page || te.Fragment != "bad" || te.Suggestion != "Title" {
				t.Errorf("RegisterPage = %v, want a finding in page %q fragment \"bad\"", err, test.page)
			}
		})
	}
}
