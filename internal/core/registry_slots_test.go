package core

import (
	"errors"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// A template that calls a slot its fragment does not declare fails at
// registration, not on the first request that renders it: the name is a literal
// in the template and the declarations are in Go, so both are known at startup.
func TestRegisterPage_RejectsASlotTheFragmentDoesNotDeclare(t *testing.T) {
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
			app := newTestAppWith(t, files, nil)
			page := newHomePage()
			page.ContentFragment.TemplatePath = "pages/recipe.html"
			page.ContentFragment.Slots = map[string]*types.SlotDefinition{"more": {Name: "more"}}

			err := app.RegisterPage(page)
			if !errors.Is(err, types.ErrUnknownSlot) {
				t.Fatalf("RegisterPage = %v, want ErrUnknownSlot", err)
			}
			for _, want := range []string{`page "home"`, `fragment "home-content"`, `"aside"`, `[more]`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %s", err, want)
				}
			}
		})
	}
}

// A slot named by anything but a literal cannot be checked before it renders, so
// it is left to the render; a declared one called by its literal name passes.
func TestRegisterPage_AcceptsSlotsItCannotOrNeedNotReject(t *testing.T) {
	files := defaultTemplates()
	files["pages/recipe.html"] = `{{slot "more"}}{{slot .Which}}`
	app := newTestAppWith(t, files, nil)
	page := newHomePage()
	page.ContentFragment.TemplatePath = "pages/recipe.html"
	page.ContentFragment.Slots = map[string]*types.SlotDefinition{"more": {Name: "more"}}

	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage = %v, want nil", err)
	}
}
