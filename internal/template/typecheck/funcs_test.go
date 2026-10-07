package typecheck

import (
	"fmt"
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

func TestCheck_ArgumentTypes(t *testing.T) {
	funcs := template.FuncMap{
		"pick": pick, "upper": strings.ToUpper,
		"i8":       func(n int8) int8 { return n },
		"uint":     func(n uint) uint { return n },
		"f64":      func(n float64) float64 { return n },
		"c128":     func(n complex128) complex128 { return n },
		"anyf":     func(v any) any { return v }, // any: a parameter text/template fills with an ideal constant
		"ptrUser":  func(u *user) string { return "p" },
		"valUser":  func(u user) string { return u.Name },
		"stringer": func(s fmt.Stringer) string { return "s" },
		"variadic": func(s string, n ...int) string { return s },
	}
	argsType := reflect.TypeFor[argData]()
	tests := []struct {
		name string
		src  string
		dot  reflect.Type
		want []string
	}{
		{"printf format constant", `{{printf 1}}`, argsType,
			[]string{"t.html:1 argument 1 of printf: expected string; found 1"}},
		{"printf format string", `{{printf "%d" 1}}`, argsType, nil},
		{"number for a string", `{{upper 1}}`, argsType,
			[]string{"t.html:1 argument 1 of upper: expected string; found 1"}},
		{"string for a string", `{{upper "x"}}`, argsType, nil},
		{"int field for a string", `{{upper .Count}}`, argsType,
			[]string{"t.html:1 argument 1 of upper: wrong type for value; expected string; got int"}},
		{"string field for a string", `{{upper .Title}}`, argsType, nil},
		{"integer constant is not range-checked", `{{i8 300}}`, argsType, nil},
		{"small integer", `{{i8 3}}`, argsType, nil},
		{"parenthesised constant is an int", `{{i8 (1)}}`, argsType,
			[]string{"t.html:1 argument 1 of i8: wrong type for value; expected int8; got int"}},
		{"negative for unsigned", `{{uint -1}}`, argsType,
			[]string{"t.html:1 argument 1 of uint: expected unsigned integer; found -1"}},
		{"positive for unsigned", `{{uint 1}}`, argsType, nil},
		{"integer for a float", `{{f64 1}}`, argsType, nil},
		{"float for a float", `{{f64 1.5}}`, argsType, nil},
		{"integer for a complex", `{{c128 1}}`, argsType,
			[]string{"t.html:1 argument 1 of c128: expected complex; found 1"}},
		{"integer for any", `{{anyf 1}}`, argsType, nil},
		{"overflow for any", `{{anyf 9223372036854775808}}`, argsType,
			[]string{"t.html:1 9223372036854775808 overflows int"}},
		{"nil for a string", `{{upper nil}}`, argsType,
			[]string{"t.html:1 argument 1 of upper: cannot assign nil to string"}},
		{"nil for a pointer", `{{ptrUser nil}}`, argsType, nil},
		{"unaddressable value for a pointer", `{{ptrUser .Owner}}`, argsType,
			[]string{"t.html:1 argument 1 of ptrUser: expected *typecheck.user, and this typecheck.user is not addressable: pass the data as a pointer, or reach the value through a slice"}},
		{"addressable value for a pointer", `{{ptrUser .Owner}}`, reflect.PointerTo(argsType), nil},
		{"slice element for a pointer", `{{range .Cards}}{{ptrUser .Owner}}{{end}}`, argsType, nil},
		{"map value may be absent", `{{ptrUser .Users.u}}`, argsType, nil},
		{"method of a map value that may be absent", `{{.Datas.d.Take "x"}}`, argsType, nil},
		{"pointer for a value", `{{valUser .Author}}`, argsType, nil},
		{"implements the interface", `{{stringer .Stamp}}`, argsType, nil},
		{"does not implement the interface", `{{stringer .Owner}}`, argsType,
			[]string{"t.html:1 argument 1 of stringer: wrong type for value; expected fmt.Stringer; got typecheck.user"}},
		{"constant for an interface with methods", `{{stringer "x"}}`, argsType,
			[]string{"t.html:1 argument 1 of stringer: a constant cannot be given as a fmt.Stringer"}},
		{"pointer receiver implements when addressable", `{{stringer .PStamp}}`, reflect.PointerTo(argsType), nil},
		{"variadic element", `{{variadic "a" 1 2}}`, argsType, nil},
		{"variadic element wrong", `{{variadic "a" .Title}}`, argsType,
			[]string{"t.html:1 argument 2 of variadic: wrong type for value; expected int; got string"}},
		{"method parameter", `{{.Take "x"}}`, argsType,
			[]string{"t.html:1 argument 1 of Take: expected integer; found \"x\""}},
		{"method parameter fits", `{{.Take 3}}`, argsType, nil},
		{"method variadic", `{{.Many "," "x"}}`, argsType,
			[]string{"t.html:1 argument 2 of Many: expected integer; found \"x\""}},
		{"piped value", `{{.Count | upper}}`, argsType,
			[]string{"t.html:1 argument 1 of upper: wrong type for value; expected string; got int"}},
		{"piped string", `{{.Title | upper}}`, argsType, nil},
		{"piped into a method", `{{.Title | .Take}}`, argsType,
			[]string{"t.html:1 argument 1 of Take: wrong type for value; expected int8; got string"}},
		{"first failing argument stops the call", `{{variadic 1 .Nope}}`, argsType,
			[]string{"t.html:1 argument 1 of variadic: expected string; found 1"}},
		{"unknown value is not judged", `{{upper .Any}}`, argsType, nil},
		{"call converts an integer", `{{call .FnI8 .Count}}`, argsType, nil},
		{"call does not convert to float", `{{call .Fn64 1}}`, argsType,
			[]string{"t.html:1 argument 1 of the function call calls: value has type int; should be float64"}},
		{"call takes no address", `{{call .FnUser .Owner}}`, reflect.PointerTo(argsType),
			[]string{"t.html:1 argument 1 of the function call calls: value has type typecheck.user; should be *typecheck.user"}},
		{"call follows no pointer", `{{call .FnVal .Author}}`, argsType,
			[]string{"t.html:1 argument 1 of the function call calls: value has type *typecheck.user; should be typecheck.user"}},
		{"index map by the wrong key", `{{index .ByNum "a"}}`, argsType,
			[]string{"t.html:1 index of type map[int]string by a key of type string; should be int"}},
		{"index map by a convertible integer", `{{index .ByNum .Small}}`, argsType, nil},
		{"index slice by a string", `{{index .Cards "a"}}`, argsType,
			[]string{"t.html:1 index by a value of type string, which is not an integer"}},
		{"slice of an int", `{{slice .Count}}`, argsType,
			[]string{"t.html:1 slice of type int, which is not a string, a slice or an array"}},
		{"slice of a string", `{{slice .Title 1}}`, argsType, nil},
		{"slice of a slice", `{{slice .Cards 0}}`, argsType, nil},
		{"slice of a struct pointer", `{{slice .Author}}`, argsType,
			[]string{"t.html:1 slice of type typecheck.user, which is not a string, a slice or an array"}},
		{"slice a string by three", `{{slice .Title 0 1 1}}`, argsType,
			[]string{"t.html:1 slice of a string takes at most two indexes"}},
		{"slice by four", `{{slice .Cards 0 0 0 0}}`, argsType,
			[]string{"t.html:1 slice takes at most three indexes, not 4"}},
		{"slice by a string", `{{slice .Cards "a"}}`, argsType,
			[]string{"t.html:1 slice by a value of type string, which is not an integer"}},
		{"slice an unaddressable array", `{{slice .Fixed 0}}`, argsType,
			[]string{"t.html:1 slice of a [2]typecheck.card that is not addressable: pass the data as a pointer, or reach the array through a slice"}},
		{"slice an addressable array", `{{slice .Fixed 0}}`, reflect.PointerTo(argsType), nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := checker(t, map[string]string{"t.html": test.src}, funcs)
			var got []string
			for _, f := range c.Check("t.html", Dot{Type: test.dot}) {
				got = append(got, describe(f))
			}
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Errorf("Check(%q)\n got: %q\nwant: %q", test.src, got, test.want)
			}
		})
	}
}
