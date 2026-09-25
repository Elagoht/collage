package core

import (
	"errors"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// A fragment bound into a slot its template never calls fails at registration, not
// as a section silently missing from every render: the calls are literals in the
// template and the bindings are in Go, so both are known at startup. Calls count
// wherever the fragment's template makes them.
func TestRegisterPage_RejectsABindingTheTemplateNeverCalls(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		template string
		partial  string
	}{
		{name: "called directly", template: `<main>{{slot "aside"}}</main>`},
		{name: "inside a branch", template: `{{if .}}{{range .}}{{with .}}{{slot "aside"}}{{end}}{{end}}{{end}}`},
		{name: "in a template it includes", template: `{{template "partials/aside.html" .}}`, partial: `{{slot "aside"}}`},
		{name: "in a block it defines", template: `{{block "side" .}}{{slot "aside"}}{{end}}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			files := defaultTemplates()
			files["pages/recipe.html"] = testCase.template
			if testCase.partial != "" {
				files["partials/aside.html"] = testCase.partial
			}
			register := func(slot string) error {
				app := newTestAppWith(t, files, nil)
				page := newHomePage()
				page.ContentFragment.TemplatePath = "pages/recipe.html"
				child := &types.Fragment{Name: "note", TemplatePath: "pages/about.html"}
				if err := page.ContentFragment.Bind(slot, child); err != nil {
					t.Fatalf("Bind: %v", err)
				}
				return app.RegisterPage(page)
			}

			if err := register("aside"); err != nil {
				t.Fatalf("binding the slot the template calls: RegisterPage = %v, want nil", err)
			}
			err := register("asdie")
			if !errors.Is(err, types.ErrUnknownSlot) {
				t.Fatalf("binding a slot the template never calls: RegisterPage = %v, want ErrUnknownSlot", err)
			}
			for _, want := range []string{`page "home"`, `fragment "home-content"`, `"asdie"`, `[aside]`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %s", err, want)
				}
			}
		})
	}
}

// A slot the template calls needs no declaration, and one it names by anything but
// a literal may be any slot, so its fragment's bindings are left to the render.
func TestRegisterPage_AcceptsSlotsItCannotOrNeedNotReject(t *testing.T) {
	files := defaultTemplates()
	files["pages/recipe.html"] = `{{slot "more"}}{{slot .Which}}`
	app := newTestAppWith(t, files, nil)
	page := newHomePage()
	page.ContentFragment.TemplatePath = "pages/recipe.html"
	child := &types.Fragment{Name: "note", TemplatePath: "pages/about.html"}
	if err := page.ContentFragment.Bind("elsewhere", child); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage = %v, want nil", err)
	}
}
