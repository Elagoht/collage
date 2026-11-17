package core

import (
	"strings"
	"testing"
)

// html/template escapes a template on its first execution, so a template it cannot
// escape parses cleanly and fails on the first request — for an optional fragment,
// as a section that is silently missing. Registration runs the escaper, so the
// same mistakes fail at startup.
func TestRegisterPage_RejectsATemplateHTMLCannotEscape(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		template string
	}{
		{name: "unclosed script string", template: `<script>var x = "{{.}}`},
		{name: "branches end in different attribute contexts", template: `<a {{if .}}href="{{else}}title='{{end}}x">`},
		{name: "ends inside an attribute", template: `<a href="{{.}}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			files := defaultTemplates()
			files["pages/recipe.html"] = testCase.template
			app := newTestAppWith(t, files, nil)
			page := newHomePage()
			page.ContentFragment.TemplatePath = "pages/recipe.html"
			err := app.RegisterPage(page)
			if err == nil {
				t.Fatal("RegisterPage accepted a template that fails on its first render")
			}
			for _, want := range []string{"pages/recipe.html", `page "home"`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %s", err, want)
				}
			}
		})
	}
}

// A partial that is only ever included from inside an attribute is not a fragment
// root, so it is never executed on its own and registration does not hold it to
// the context it has when it stands alone.
func TestRegisterPage_APartialIncludedInAnAttributeIsNotProbedAlone(t *testing.T) {
	files := defaultTemplates()
	files["pages/recipe.html"] = `<a href="{{template "partials/href.html" .}}">x</a>`
	files["partials/href.html"] = `/recipes/{{.}}`
	app := newTestAppWith(t, files, nil)
	page := newHomePage()
	page.ContentFragment.TemplatePath = "pages/recipe.html"
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage = %v", err)
	}
}
