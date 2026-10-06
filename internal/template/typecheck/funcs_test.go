package typecheck

import (
	"html/template"
	"reflect"
	"strings"
	"testing"
	"text/template/parse"
)

func pick(n int) []card { return make([]card, n) }

func TestCheck_Funcs(t *testing.T) {
	funcs := template.FuncMap{
		"pick":   pick,
		"upper":  strings.ToUpper,
		"stand":  func(...any) (any, error) { return nil, nil }, // any: the stand-in a plugin's render function is parsed with
		"urlFor": func(name string, _ ...reflect.Value) (string, error) { return name, nil },
	}
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"result type flows", `{{range pick 2}}{{.Name}}{{end}}`, nil},
		{"result type wrong field", `{{range pick 2}}{{.Nme}}{{end}}`,
			[]string{"t.html:1 type typecheck.card has no field or method Nme [Name]"}},
		{"function argument count", `{{upper}}`,
			[]string{"t.html:1 wrong number of arguments for upper: want 1, got 0"}},
		{"piped final argument counts", `{{.Title | upper}}`, nil},
		{"arguments are walked", `{{upper .Titl}}`,
			[]string{"t.html:1 type typecheck.board has no field or method Titl [Title]"}},
		{"plugin stand-in is unknown", `{{(stand 1 2).Anything}}`, nil},
		{"reflect.Value variadic", `{{urlFor "post" "id" 1}}`, nil},
		{"len", `{{len .Cards}}`, nil},
		{"len of struct", `{{len .Owner}}`,
			[]string{"t.html:1 len of type typecheck.user"}},
		{"index slice", `{{(index .Cards 0).Name}}`, nil},
		{"index slice element is addressable", `{{(index .Cards 0).Owner.Edit}}`, nil},
		{"index map", `{{(index .ByCol "a").Nme}}`,
			[]string{"t.html:1 type typecheck.card has no field or method Nme [Name]"}},
		{"index struct", `{{index .Owner 0}}`,
			[]string{"t.html:1 cannot index into type typecheck.user"}},
		{"slice keeps the type", `{{range slice .Cards 1}}{{.Name}}{{end}}`, nil},
		{"comparisons are bool", `{{if eq .Title "x"}}{{end}}`, nil},
		{"print is string", `{{(print .Title).Nope}}`,
			[]string{"t.html:1 type string has no field or method Nope"}},
		{"call is unknown", `{{(call .Seq).Whatever}}`, nil},
		{"and/or are unknown", `{{(and .Title .Count).Whatever}}`, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := checker(t, map[string]string{"t.html": test.src}, funcs)
			var got []string
			for _, f := range c.Check("t.html", Dot{Type: boardType}) {
				got = append(got, describe(f))
			}
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Errorf("Check(%q)\n got: %q\nwant: %q", test.src, got, test.want)
			}
		})
	}
}

func TestCheck_EscaperFunctionsIgnored(t *testing.T) {
	set := template.Must(template.New("t.html").Parse(`<a href="{{.Title}}">{{.Title}}</a>`))
	if err := set.Execute(new(strings.Builder), board{}); err != nil {
		t.Fatal(err)
	}
	c := &Checker{Lookup: func(name string) *parse.Tree { return set.Lookup(name).Tree }}
	if got := c.Check("t.html", Dot{Type: boardType}); len(got) != 0 {
		t.Errorf("escaped tree: %+v, want none", got)
	}
}
