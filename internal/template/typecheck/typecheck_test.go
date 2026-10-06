package typecheck

import (
	"html/template"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"text/template/parse"
)

type user struct{ Name string }

func (u user) Display() string { return u.Name }
func (u *user) Edit() string   { return "edit" }

type slug string

type layout struct{ U user }

type page struct{ *layout }

type pm map[string]string

func (m *pm) Foo() string { return "foo" }

type inner struct{ Edit string }

type outer struct{ inner }

func (o *outer) Edit() string { return "edit" }

type comment struct {
	Body string
	By   user
}

type embedded struct{ Promoted string }

type post struct {
	Title    string
	secret   string
	Author   *user
	Owner    user
	Tags     []string
	Comments []comment
	Meta     map[string]string
	ByID     map[slug]comment
	Loose    map[string]any // any: a value the checker must not look into
	Extra    any            // any: a value the checker must not look into
	embedded
}

func (p post) URL() string                             { return "/" + p.Title }
func (p post) Two(a, b string) string                  { return a + b }
func (p post) Bad() (string, string)                   { return "", "" }
func (p post) Err() (string, error)                    { return "", nil }
func (p post) Join(sep string, parts ...string) string { return strings.Join(parts, sep) }

// checker parses files into one html/template set, as the engine does, and
// returns a Checker over it.
func checker(t *testing.T, files map[string]string, funcs template.FuncMap) *Checker {
	t.Helper()
	set := template.New("").Funcs(funcs)
	for name, src := range files {
		if _, err := set.New(name).Parse(src); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
	}
	types := make(map[string]reflect.Type, len(funcs))
	for name, fn := range funcs {
		types[name] = reflect.TypeOf(fn)
	}
	return &Checker{
		Lookup: func(name string) *parse.Tree {
			if found := set.Lookup(name); found != nil {
				return found.Tree
			}
			return nil
		},
		Funcs: types,
	}
}

// reasons runs src as "t.html" against dot and returns each finding as
// "t.html:line reason [suggestion]".
func reasons(t *testing.T, src string, dot reflect.Type) []string {
	t.Helper()
	c := checker(t, map[string]string{"t.html": src}, nil)
	var out []string
	for _, f := range c.Check("t.html", Dot{Type: dot}) {
		out = append(out, describe(f))
	}
	return out
}

// describe writes a finding as "file:line reason [suggestion]". Columns are left
// to Task 4's test, which compares them with text/template's own errors.
func describe(f Finding) string {
	line := f.Template + ":" + strconv.Itoa(f.Line) + " " + f.Reason
	if f.Suggestion != "" {
		line += " [" + f.Suggestion + "]"
	}
	return line
}

var postType = reflect.TypeFor[post]()
var postPtr = reflect.TypeFor[*post]()

func TestCheck_FieldsMethodsMaps(t *testing.T) {
	tests := []struct {
		name string
		src  string
		dot  reflect.Type
		want []string
	}{
		{"field", `{{.Title}}`, postType, nil},
		{"missing field suggests", `{{.Titel}}`, postType,
			[]string{"t.html:1 type typecheck.post has no field or method Titel [Title]"}},
		{"unexported field", `{{.secret}}`, postType,
			[]string{"t.html:1 secret is an unexported field of type typecheck.post"}},
		{"promoted field", `{{.Promoted}}`, postType, nil},
		{"through a pointer", `{{.Author.Name}}`, postType, nil},
		{"missing through a pointer", `{{.Author.Nmae}}`, postType,
			[]string{"t.html:1 type typecheck.user has no field or method Nmae [Name]"}},
		{"value method", `{{.URL}}`, postType, nil},
		{"field of a string", `{{.Title.Len}}`, postType,
			[]string{"t.html:1 type string has no field or method Len"}},
		{"field given arguments", `{{.Title "x"}}`, postType,
			[]string{"t.html:1 Title is a field of type typecheck.post, not a method, and takes no arguments"}},
		{"method argument count", `{{.Two "a"}}`, postType,
			[]string{"t.html:1 wrong number of arguments for Two: want 2, got 1"}},
		{"variadic method", `{{.Join "," "a" "b"}}`, postType, nil},
		{"variadic method too few", `{{.Join}}`, postType,
			[]string{"t.html:1 wrong number of arguments for Join: want at least 1, got 0"}},
		{"two results without error", `{{.Bad}}`, postType,
			[]string{"t.html:1 Bad returns 2 values; a template can call one that returns a value, or a value and an error"}},
		{"value and error", `{{.Err}}`, postType, nil},
		{"string-keyed map", `{{.Meta.anything}}`, postType, nil},
		{"map keyed by a named string", `{{.ByID.first}}`, postType,
			[]string{"t.html:1 type map[typecheck.slug]typecheck.comment is keyed by typecheck.slug, which .first cannot look up"}},
		{"map[string]any is unknown", `{{.Loose.a.b.c}}`, postType, nil},
		{"interface field is unknown", `{{.Extra.Whatever}}`, postType, nil},
		{"unknown dot reports nothing", `{{.Nope}}`, nil, nil},

		{"pointer method on data passed by value", `{{.Owner.Edit}}`, postType,
			[]string{"t.html:1 method Edit has a pointer receiver, and this typecheck.user is not addressable: pass the data as a pointer, or reach the value through a slice"}},
		{"pointer method on data passed by pointer", `{{.Owner.Edit}}`, postPtr, nil},
		{"pointer method through a pointer field", `{{.Author.Edit}}`, postType, nil},
		{"value method on a map element", `{{.x.By.Display}}`, reflect.TypeFor[map[string]comment](), nil},
		{"pointer method on a string-map element", `{{.x.By.Edit}}`, reflect.TypeFor[map[string]comment](),
			[]string{"t.html:1 method Edit has a pointer receiver, and this typecheck.user is not addressable: pass the data as a pointer, or reach the value through a slice"}},
		{"pointer method through an embedded pointer", `{{.U.Edit}}`, reflect.TypeFor[page](), nil},
		{"pointer method on a map falls back to the key", `{{.Foo}}`, reflect.TypeFor[pm](), nil},
		{"pointer method shadowed by a promoted field", `{{.Edit}}`, reflect.TypeFor[outer](), nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := reasons(t, test.src, test.dot)
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Errorf("Check(%q)\n got: %q\nwant: %q", test.src, got, test.want)
			}
		})
	}
}

func TestCheck_ExprAndOrder(t *testing.T) {
	c := checker(t, map[string]string{"t.html": "{{.Zz}}\n  {{.Aa}} {{.Title}}"}, nil)
	got := c.Check("t.html", Dot{Type: postType})
	if len(got) != 2 {
		t.Fatalf("findings = %+v, want 2", got)
	}
	if got[0].Line != 1 || got[0].Expr != "{{.Zz}}" || got[1].Line != 2 || got[1].Col != 4 || got[1].Expr != "{{.Aa}}" {
		t.Errorf("findings = %+v, want .Zz at 1 then .Aa at 2:4", got)
	}
}

func TestCheck_ExprIsNotTruncated(t *testing.T) {
	c := checker(t, map[string]string{"t.html": `{{.Author.Nmaaaaaaaaaaaaaaaaaaaaaaaaaa}}`}, nil)
	got := c.Check("t.html", Dot{Type: postType})
	if len(got) != 1 || got[0].Expr != "{{.Author.Nmaaaaaaaaaaaaaaaaaaaaaaaaaa}}" {
		t.Errorf("findings = %+v, want the whole expression", got)
	}
}

func TestCheck_UnknownTemplate(t *testing.T) {
	c := checker(t, map[string]string{"t.html": `x`}, nil)
	if got := c.Check("nope.html", Dot{Type: postType}); len(got) != 0 {
		t.Errorf("Check(unknown) = %+v, want none", got)
	}
}
