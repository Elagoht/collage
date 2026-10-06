package typecheck

import (
	"html/template"
	"io"
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
		{"call checks its function's shape", `{{(call .Seq).Whatever}}`,
			[]string{"t.html:1 wrong number of arguments for call: want 1, got 0", "t.html:1 call returns 0 values; a template can call one that returns a value, or a value and an error"}},
		{"call of an unknown value is unknown", `{{(call (stand 1)).Whatever}}`, nil},
		{"builtin argument count", `{{len .Cards .Cards}}`,
			[]string{"t.html:1 wrong number of arguments for len: want 1, got 2"}},
		{"no findings inside a call of the wrong shape", `{{upper .Nope "b"}}`,
			[]string{"t.html:1 wrong number of arguments for upper: want 1, got 2"}},
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

func TestCheck_ShortCircuitsAndLiteralConditions(t *testing.T) {
	nope := "t.html:1 type typecheck.board has no field or method Nope"
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"and stops at false", `{{and false .Nope}}`, nil},
		{"or stops at true", `{{or true .Nope}}`, nil},
		{"and walks past true", `{{and true .Nope}}`, []string{nope}},
		{"or walks past false", `{{or false .Nope}}`, []string{nope}},
		{"and with a non-constant guard", `{{and .Title .Nope}}`, []string{nope}},
		{"if false skips the body", `{{if false}}{{.Nope}}{{end}}`, nil},
		{"if false walks the else", `{{if false}}{{else}}{{.Nope}}{{end}}`, []string{nope}},
		{"if true skips the else", `{{if true}}{{else}}{{.Nope}}{{end}}`, nil},
		{"if true walks the body", `{{if true}}{{.Nope}}{{end}}`, []string{nope}},
		{"with false skips the body", `{{with false}}{{.Nope}}{{end}}`, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := template.Must(template.New("x").Parse(test.src)).Execute(io.Discard, board{Title: "x"}); (err != nil) != (len(test.want) > 0) {
				t.Fatalf("html/template Execute(%q) = %v, disagrees with the expectation %q", test.src, err, test.want)
			}
			got := reasons(t, test.src, boardType)
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Errorf("Check(%q)\n got: %q\nwant: %q", test.src, got, test.want)
			}
		})
	}
}

func TestCheck_FunctionFindingIsTheWholeCommand(t *testing.T) {
	c := checker(t, map[string]string{"t.html": `{{len (index .Cards 0).Owner}}`}, nil)
	got := c.Check("t.html", Dot{Type: boardType})
	if len(got) != 1 || got[0].Expr != "{{len (index .Cards 0).Owner}}" {
		t.Errorf("findings = %+v, want one with the whole command as Expr", got)
	}
}
