package typecheck

import (
	"fmt"
	"html/template"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// filledBoard and filledPost have every pointer set and every collection
// non-empty, so an execution error can only come from what the checker judges,
// never from a nil it does not model. Each call makes a new board: ranging over
// its channel drains it.
//
// Every map key a case reads is present, too. The property this test holds the
// checker to — a finding exactly when execution fails — rests on that: a key
// absent at runtime gives an invalid value that ends the chain silently, so
// {{.Meta.absent.Nope}} renders nothing while the checker, which knows only the
// map's element type, reports .Nope.
//
// wrapper hands out reflect.Values, which text/template unwraps when a method
// or function returns one, and leaves alone in a field.
func filledBoard() board {
	c := card{Name: "c", Owner: user{Name: "o"}}
	ch := make(chan card, 1)
	ch <- c
	close(ch)
	cards := []card{c}
	fixed := [2]card{c, c}
	return board{
		Cards: cards, Fixed: fixed, ByCol: map[string]card{"a": c},
		Count: 2, Seq: func(yield func(card) bool) { yield(c) }, Title: "t", Owner: user{Name: "o"},
		Ch: ch, Seq2: func(yield func(card, int) bool) { yield(c, 1) }, PS: &cards, PFix: &fixed,
	}
}

type wrapper struct {
	F  reflect.Value
	Fn func() reflect.Value
}

func (wrapper) RV() reflect.Value             { return reflect.ValueOf(user{Name: "r"}) }
func (wrapper) RVErr() (reflect.Value, error) { return reflect.ValueOf(&user{Name: "r"}), nil }

func wrap() reflect.Value { return reflect.ValueOf(user{Name: "w"}) }

func filledWrapper() wrapper {
	return wrapper{F: reflect.ValueOf(user{Name: "f"}), Fn: wrap}
}

func filledPost() post {
	return post{
		Title: "t", secret: "s", Author: &user{Name: "a"}, Owner: user{Name: "o"}, Tags: []string{"x"},
		Comments: []comment{{Body: "b"}}, Meta: map[string]string{"anything": "m"},
		ByID:  map[slug]comment{"first": {}},
		Loose: map[string]any{"a": map[string]any{"b": map[string]any{"c": 1}}}, // any: the field's own type
		Extra: user{Name: "e"}, embedded: embedded{Promoted: "p"},
	}
}

func TestCheck_AgreesWithTextTemplate(t *testing.T) {
	funcs := template.FuncMap{
		"pick": pick, "upper": strings.ToUpper, "wrap": wrap,
		"i8":   func(n int8) int8 { return n },
		"f64":  func(n float64) float64 { return n },
		"u64":  func(n uint64) uint64 { return n },
		"anyf": func(v any) any { return v }, // any: a parameter text/template fills with an ideal constant

		// Typed parameters an argument is converted to or checked against.
		"uint":     func(n uint) uint { return n },
		"c128":     func(n complex128) complex128 { return n },
		"boolf":    func(b bool) bool { return b },
		"slugf":    func(s slug) slug { return s },
		"ptrUser":  func(u *user) string { return "p" },
		"valUser":  func(u user) string { return u.Name },
		"stringer": func(s fmt.Stringer) string { return "s" },
		"errf":     func(err error) string { return "e" },
		"cardsf":   func(c []card) int { return len(c) },
		"variadic": func(s string, n ...int) string { return s },
		"rv":       func(v reflect.Value) string { return "v" },
	}
	type differential struct {
		src  string
		data func() reflect.Value
	}
	var cases []differential
	// add registers srcs to run with what data makes; each case gets fresh data.
	add := func(data func() any, srcs ...string) { // any: the test hands each case's data to html/template as the engine does
		for _, src := range srcs {
			cases = append(cases, differential{src, func() reflect.Value { return reflect.ValueOf(data()) }})
		}
	}
	post := func() any { return filledPost() }               // any: case data
	postPtr := func() any { p := filledPost(); return &p }   // any: case data
	board := func() any { return filledBoard() }             // any: case data
	boardPtr := func() any { b := filledBoard(); return &b } // any: case data

	add(post,
		`{{.Two .Nope}}`, `{{.Title .Nope}}`, `{{.Bad .Nope}}`, `{{.Nope .Titl}}`,
		`{{(.Two "a" "b").Nope}}`, `{{.Join "," .Titl}}`, `{{(.Join "," "a" "b").Nope}}`,
		`{{.Title}}`, `{{.Titel}}`, `{{.Promoted}}`, `{{.Author.Name}}`, `{{.Author.Nmae}}`,
		`{{.URL}}`, `{{.Title.Len}}`, `{{.Title "x"}}`, `{{.Two "a"}}`, `{{.Two "a" "b"}}`,
		`{{.Join "," "a"}}`, `{{.Join}}`, `{{.Bad}}`, `{{.Err}}`, `{{.Meta.anything}}`, `{{.ByID.first}}`,
		`{{.Loose.a.b.c}}`, `{{.Owner.Edit}}`, `{{.Author.Edit}}`, `{{.Owner.Display}}`,
		`{{.secret}}`, `{{.Extra.Name}}`, `{{.URL.Nope}}`,
	)
	add(postPtr, `{{.Owner.Edit}}`, `{{with .Owner}}{{.Edit}}{{end}}`, `{{$o := .Owner}}{{$o.Edit}}`)
	add(func() any { return map[string]comment{"x": {}} }, `{{.x.By.Edit}}`, `{{.x.By.Display}}`) // any: case data
	add(board,
		`{{range .Cards}}{{.Name}}{{end}}`, `{{range .Cards}}{{.Nam}}{{end}}`,
		`{{range $i, $c := .Cards}}{{$c.Name}}{{end}}`, `{{range $k, $v := .ByCol}}{{$v.Name}}{{end}}`,
		`{{range .Count}}{{.}}{{end}}`, `{{range $i, $v := .Count}}{{end}}`, `{{range .Seq}}{{.Name}}{{end}}`,
		`{{range .Title}}{{end}}`, `{{range .Owner}}{{end}}`,
		`{{range .Cards}}{{.Owner.Edit}}{{end}}`, `{{range .ByCol}}{{.Owner.Edit}}{{end}}`,
		`{{range .Fixed}}{{.Owner.Edit}}{{end}}`,
		`{{with .Owner}}{{.Name}}{{end}}`, `{{with .Owner}}{{.Title}}{{end}}`, `{{with .Owner}}{{.Edit}}{{end}}`,
		`{{$o := .Owner}}{{$o.Name}}`, `{{$o := .Owner}}{{$o.Nme}}`,
		`{{range pick 2}}{{.Name}}{{end}}`, `{{upper .Title}}`,
		`{{len .Cards}}`, `{{len .Owner}}`, `{{(index .Cards 0).Name}}`, `{{(index .Cards 0).Owner.Edit}}`,
		`{{(index .ByCol "a").Owner.Edit}}`, `{{index .Owner 0}}`, `{{range slice .Cards 0}}{{.Name}}{{end}}`,
		`{{(print .Title).Nope}}`,

		// Channels, iter.Seq and iter.Seq2.
		`{{range .Ch}}{{.Name}}{{end}}`, `{{range .Ch}}{{.Nam}}{{end}}`,
		`{{range $i, $c := .Ch}}{{$c.Name}}{{$i}}{{end}}`, `{{range $i, $c := .Ch}}{{$c.Nam}}{{end}}`,
		`{{range $a, $b := .Seq}}{{end}}`,
		`{{range .Seq2}}{{.Name}}{{end}}`, `{{range .Seq2}}{{.Nope}}{{end}}`,
		`{{range $k := .Seq2}}{{$k.Name}}{{end}}`, `{{range $k := .Seq2}}{{$k.Nope}}{{end}}`,
		`{{range $k, $v := .Seq2}}{{$k.Name}}{{$v}}{{end}}`, `{{range $k, $v := .Seq2}}{{$v.Nope}}{{end}}`,
		`{{range .PS}}{{.Owner.Edit}}{{end}}`, `{{range .PFix}}{{.Owner.Edit}}{{end}}`,

		// and, or and literal conditions decide what runs.
		`{{and false .Nope}}`, `{{or true .Nope}}`, `{{and true .Nope}}`, `{{or false .Nope}}`,
		`{{if false}}{{.Nope}}{{end}}`, `{{if false}}{{else}}{{.Nope}}{{end}}`,
		`{{if true}}{{else}}{{.Nope}}{{end}}`, `{{if true}}{{.Nope}}{{end}}`,
		`{{with false}}{{.Nope}}{{end}}`, `{{with false}}{{else}}{{.Nope}}{{end}}`,
		`{{if not true}}{{.Nope}}{{end}}`, `{{if not false}}{{.Nope}}{{end}}`,
		`{{if not true}}{{else}}{{.Nope}}{{end}}`, `{{if not 0}}{{else}}{{.Nope}}{{end}}`,
		`{{if not nil}}{{.Nope}}{{end}}`, `{{if not ""}}{{.Nope}}{{end}}`,
		`{{and 0 .Nope}}`, `{{and 1 .Nope}}`, `{{or 1 .Nope}}`, `{{or 0 .Nope}}`,
		`{{and "" .Nope}}`, `{{and "a" .Nope}}`, `{{or "a" .Nope}}`, `{{or "" .Nope}}`,
		`{{and nil .Nope}}`, `{{or nil .Nope}}`, `{{and 0.0 .Nope}}`, `{{and 0.5 .Nope}}`,
		`{{and 0x0 .Nope}}`, `{{and 0x1E .Nope}}`, `{{and 'a' .Nope}}`, `{{or 'a' .Nope}}`,
		`{{and 0i .Nope}}`, `{{or 1i .Nope}}`, `{{and 1e0 0 .Nope}}`, `{{and 1 2 .Nope}}`,
		`{{and (not true) .Nope}}`, `{{or (not 0) .Nope}}`, `{{and (not 1) .Nope}}`, `{{or (not "a") .Nope}}`,
		`{{and .Title 0 .Nope}}`, `{{or .Count 1 .Nope}}`,
		`{{if 0}}{{.Nope}}{{end}}`, `{{if 1}}{{.Nope}}{{end}}`, `{{if ""}}{{.Nope}}{{end}}`,
		`{{if "a"}}{{.Nope}}{{end}}`, `{{if 0}}{{else}}{{.Nope}}{{end}}`, `{{if 2}}{{else}}{{.Nope}}{{end}}`,
		`{{with ""}}{{.Nope}}{{end}}`, `{{with "a"}}{{.Len}}{{end}}`, `{{with "a"}}{{.}}{{end}}`,
		`{{with 0}}{{else}}{{.Nope}}{{end}}`, `{{with 0}}{{.Nope}}{{end}}`, `{{with 0.0}}{{.Nope}}{{end}}`,
		`{{if $x := false}}{{.Nope}}{{end}}`, `{{if $x := 0}}{{.Nope}}{{end}}`, `{{if $x := ""}}{{.Nope}}{{end}}`,
		`{{with $x := ""}}{{.Nope}}{{end}}`, `{{with $x := 0}}{{.Nope}}{{else}}{{$x}}{{end}}`,
		`{{$x := 1}}{{if $x = false}}{{.Nope}}{{end}}`, `{{$x := 1}}{{if $x = 0}}{{.Nope}}{{end}}`,
		`{{$x := 1}}{{with $x = ""}}{{.Nope}}{{end}}`, `{{if $x := not true}}{{.Nope}}{{end}}`,
		`{{if $x := true}}{{.Nope}}{{end}}`, `{{if $x := 1}}{{.Nope}}{{end}}`, `{{with $x := "a"}}{{.Nope}}{{end}}`,
		`{{$x := 0}}{{if $x = true}}{{.Nope}}{{end}}`, `{{$x := 0}}{{if $x = 1}}{{.Nope}}{{end}}`,
		`{{if $x := false}}{{else}}{{.Nope}}{{end}}`, `{{if $x := 1}}{{else}}{{.Nope}}{{end}}`,

		// Builtins and FuncMap functions.
		`{{len .Count}}`, `{{len .Title}}`, `{{len .ByCol}}`, `{{index .Count 0}}`, `{{index .Cards 0 0}}`,
		`{{(index .Fixed 0).Owner.Edit}}`, `{{(index .PFix 0).Owner.Edit}}`, `{{(index .PS 0).Owner.Edit}}`,
		`{{upper}}`, `{{upper "a" "b"}}`, `{{.Title | upper}}`, `{{"a" | upper "b"}}`, `{{pick}}`,
		`{{(pick 1).Nope}}`, `{{(index (pick 1) 0).Owner.Edit}}`,

		// Where text/template names a failure after evaluating arguments.
		`{{range (index .Cards 0).Owner}}{{end}}`, `{{range $i, $v := len .Cards}}{{end}}`,
		`{{range $i, $v := .Count | print}}{{end}}`, `{{(.Owner.Display).Nope}}`,
		`{{upper .Owner.Nope}}`, `{{with $x := .Owner}}{{$x.Nope}}{{end}}`, `{{$.Owner.Nope}}`,
		`{{define "row"}}{{.Nme}}{{end}}{{range .Cards}}{{template "row" .}}{{end}}`,
		`{{define "row"}}{{.Name}}{{end}}{{template "row" .Owner}}{{template "row" .}}`,

		// Builtin argument counts, checked before the arguments run.
		`{{len}}`, `{{len .Cards .Cards}}`, `{{not}}`, `{{not .Title}}`, `{{not .Title .Count}}`,
		`{{and}}`, `{{and .Title}}`, `{{or}}`, `{{or .Title .Count}}`,
		`{{index}}`, `{{index .Cards}}`, `{{slice}}`, `{{slice .Cards}}`, `{{slice .Cards 0 1}}`,
		`{{call}}`, `{{call .Seq (index .Cards 0) | not}}`, `{{call .Seq}}`, `{{call .Title}}`,
		`{{(call .Seq).Whatever}}`,
		`{{eq}}`, `{{eq .Count}}`, `{{eq .Count 1}}`, `{{eq .Count 1 2}}`,
		`{{ne .Count}}`, `{{ne .Count 1}}`, `{{ne .Count 1 2}}`, `{{lt .Count}}`, `{{lt .Count 1}}`,
		`{{le .Count 1 2}}`, `{{le .Count 1}}`, `{{gt}}`, `{{gt .Count 1}}`, `{{ge .Count}}`, `{{ge .Count 1}}`,
		`{{print}}`, `{{print .Title 1}}`, `{{printf}}`, `{{printf "%s" .Title}}`, `{{println}}`,
		`{{.Title | len}}`, `{{.Title | len .Title}}`, `{{.Title | not .Count}}`, `{{.Count | eq}}`,
		`{{js}}`, `{{js .Title 1}}`,

		// A call of the wrong shape never evaluates its arguments.
		`{{upper .Nope "b"}}`, `{{len .Nope .Nada}}`, `{{not (index .Owner 0) 1}}`, `{{eq .Nope}}`,

		// Number constants are typed as text/template's idealConstant types
		// them; as an argument to a typed parameter they are converted instead.
		`{{$x := 1}}{{$x.Nope}}`, `{{(1).Nope}}`, `{{(1.5).Nope}}`, `{{(1i).Nope}}`, `{{('a').Nope}}`,
		`{{(0x10).Nope}}`, `{{(0x1p4).Nope}}`, `{{(1e3).Nope}}`, `{{(1_000).Nope}}`,
		`{{$x := 1}}{{len $x}}`, `{{$x := 1}}{{printf "%d" $x}}`, `{{$x := 1}}{{range $x}}{{.}}{{end}}`,
		`{{$x := 2}}{{range $i, $v := $x}}{{end}}`, `{{index (1) 0}}`, `{{1 | len}}`, `{{len 1}}`,
		`{{with 1}}{{.Nope}}{{end}}`, `{{with 1.5}}{{.Nope}}{{end}}`, `{{range 3}}{{.Nope}}{{end}}`,
		`{{range 3}}{{.}}{{end}}`, `{{$x := 1}}{{$x = 2}}{{$x.Nope}}`, `{{$x := 1}}{{$x}}`,
		`{{i8 1}}`, `{{f64 1}}`, `{{u64 18446744073709551615}}`, `{{(i8 1).Nope}}`, `{{anyf 1}}`,
		`{{9223372036854775808}}`, `{{print 9223372036854775808}}`, `{{anyf 9223372036854775808}}`,
		`{{len 9223372036854775808}}`, `{{eq 9223372036854775808 1}}`, `{{and 9223372036854775808 1}}`,
		`{{print 1 9223372036854775808 .Nope}}`,
		`{{call 1}}`, `{{index "ab" 1}}`, `{{slice "abc" 1 2}}`, `{{nil}}`, `{{(nil).X}}`, `{{$x := nil}}`, `{{nil | print}}`, `{{print nil}}`, `{{anyf nil}}`,

		// Positions on later lines, and inside actions that span lines.
		"x\n{{range .Cards}}\n  {{.Nam}}{{end}}", "\n{{(index .ByCol\n  \"a\").Owner.Edit}}",
		"\n{{upper\n \"a\" \"b\"}}", "\n\n{{range $i, $v :=\n  .Count}}{{end}}", "{{len\n\n .Owner}}",
	)
	add(func() any { return filledWrapper() }, // any: case data
		`{{.RV.Name}}`, `{{.RVErr.Name}}`, `{{(wrap).Name}}`, `{{.RV}}`, `{{wrap | print}}`,
		`{{.F.Name}}`, `{{(call .Fn).Name}}`, `{{call .Fn}}`,
	)
	add(func() any { return numeric{} }, // any: case data
		`{{.U 18446744073709551615}}`, `{{.F 9223372036854775808}}`, `{{.I8 1}}`, `{{.F 1.5}}`,
		`{{(.U 1).Nope}}`, `{{(.I8 (1)).Nope}}`,
	)
	add(boardPtr, `{{range .Fixed}}{{.Owner.Edit}}{{end}}`, `{{(index .Fixed 0).Owner.Edit}}`, `{{.Owner.Edit}}`)

	// A *T method reached through an embedded pointer, or hidden on an
	// unaddressable value whose key or promoted field has its name.
	add(func() any { return page{&layout{U: user{Name: "u"}}} }, `{{.U.Edit}}`, `{{.U.Nope}}`, `{{.U.Name}}`) // any: case data
	add(func() any { return pm{"Foo": "x"} }, `{{.Foo}}`, `{{.Foo.Nope}}`)                                    // any: case data
	add(func() any { m := pm{"Foo": "x"}; return &m }, `{{.Foo}}`)                                            // any: case data
	add(func() any { return outer{inner{Edit: "e"}} }, `{{.Edit}}`, `{{.Edit "x"}}`)                          // any: case data
	add(func() any { return &outer{inner{Edit: "e"}} }, `{{.Edit}}`, `{{.Edit "x"}}`)                         // any: case data

	// Argument types, judged as text/template's evalArg judges them: a
	// constant by its parameter's kind, any other value by validateType's
	// rules; call, index and slice by the checks inside them.
	argsVal := func() any { return filledArgs() }          // any: case data
	argsPtr := func() any { a := filledArgs(); return &a } // any: case data
	add(argsVal,
		// Constants against the parameter's kind.
		`{{printf 1}}`, `{{printf "%d" 1}}`, `{{printf nil}}`, `{{printf true}}`, `{{printf .Count}}`, `{{printf .Title}}`,
		`{{upper 1}}`, `{{upper "x"}}`, `{{upper true}}`, `{{upper nil}}`, `{{upper 1.5}}`,
		`{{i8 300}}`, `{{i8 3}}`, `{{i8 -3}}`, `{{i8 1.5}}`, `{{i8 1.0}}`, `{{i8 1e3}}`, `{{i8 'a'}}`, `{{i8 "x"}}`,
		`{{i8 (1)}}`, `{{$x := 1}}{{i8 $x}}`, `{{i8 .Small}}`, `{{i8 .Count}}`,
		`{{uint -1}}`, `{{uint 1}}`, `{{uint 0x10}}`, `{{uint 1.5}}`, `{{uint -0}}`,
		`{{f64 1}}`, `{{f64 1.5}}`, `{{f64 'a'}}`, `{{f64 1i}}`, `{{f64 "x"}}`, `{{f64 .Ratio}}`, `{{f64 .Count}}`,
		`{{c128 1}}`, `{{c128 1i}}`, `{{c128 1.5}}`,
		`{{boolf true}}`, `{{boolf 1}}`, `{{boolf "x"}}`, `{{boolf nil}}`, `{{boolf .Flag}}`, `{{boolf .Title}}`,
		`{{anyf 1}}`, `{{anyf 9223372036854775808}}`, `{{anyf "x"}}`, `{{anyf .Owner}}`,
		`{{rv 1}}`, `{{rv nil}}`, `{{rv .Owner}}`, `{{rv 9223372036854775808}}`,
		`{{slugf "a"}}`, `{{slugf .Title}}`, `{{slugf .Slug}}`, `{{slugf 1}}`,
		`{{stringer "x"}}`, `{{stringer 1}}`, `{{stringer nil}}`, `{{stringer true}}`,
		`{{errf nil}}`, `{{ptrUser nil}}`, `{{cardsf nil}}`, `{{cardsf 1}}`, `{{cardsf "x"}}`,

		// Values of a known type against the parameter's type.
		`{{stringer .Stamp}}`, `{{stringer .Owner}}`, `{{stringer .PStamp}}`, `{{stringer .PSPtr}}`,
		`{{ptrUser .Owner}}`, `{{ptrUser .Author}}`, `{{valUser .Author}}`, `{{valUser .Owner}}`,
		`{{range .Cards}}{{ptrUser .Owner}}{{end}}`, `{{range .Users}}{{ptrUser .}}{{end}}`,
		`{{ptrUser (index .Cards 0).Owner}}`, `{{ptrUser .Users.missing}}`, `{{stringer .Users.missing}}`,
		`{{with .Owner}}{{ptrUser .}}{{end}}`, `{{$o := .Owner}}{{ptrUser $o}}`,
		`{{cardsf .Cards}}`, `{{cardsf .Fixed}}`, `{{cardsf .Owner}}`,
		`{{range .Cards}}{{upper .}}{{end}}`, `{{with .Title}}{{upper .}}{{end}}`,
		`{{upper (index .Cards 0).Name}}`, `{{upper (index .Cards 0).Owner}}`, `{{upper (pick 1)}}`,
		`{{upper (upper 1)}}`, `{{upper .Any}}`, `{{anyf .F}}`,
		`{{upper .Count .Nope}}`, "{{upper\n  .Count}}",

		// Variadic parameters take the element type.
		`{{variadic "a" 1 2}}`, `{{variadic "a" "b"}}`, `{{variadic "a" .Count}}`, `{{variadic "a" .Title}}`,
		`{{variadic 1}}`, `{{variadic "a" 1 1.5}}`, `{{variadic "a" .Small}}`,

		// The piped final value.
		`{{.Count | upper}}`, `{{.Title | upper}}`, `{{1 | upper}}`, `{{"x" | upper}}`, `{{.Count | printf}}`,
		`{{.Title | printf}}`, `{{.Count | printf "%d"}}`, `{{.Count | variadic "a"}}`, `{{.Title | variadic "a"}}`,
		`{{"a" | variadic}}`, `{{.Count | variadic}}`, `{{.Owner | ptrUser}}`, `{{.Author | valUser}}`,
		`{{.Count | .Take}}`, `{{.Title | .Take}}`, `{{.Count | upper | upper}}`,

		// Methods with typed parameters.
		`{{.Take 3}}`, `{{.Take 300}}`, `{{.Take "x"}}`, `{{.Take .Count}}`, `{{.Take .Small}}`, `{{.Take 1.5}}`,
		`{{.Name 1}}`, `{{.Name "x"}}`, `{{.Many "," 1 2}}`, `{{.Many "," "x"}}`, `{{.Many 1}}`,
		`{{.Ptr .Owner}}`, `{{.Ptr .Author}}`, `{{.Ptr nil}}`, `{{$.Take "x"}}`, `{{$a := .}}{{$a.Take "x"}}`,

		// call checks each argument as prepareArg does: assignable, or an
		// integer converted to another integer type.
		`{{call .Fn64 1.5}}`, `{{call .Fn64 1}}`, `{{call .FnI8 300}}`, `{{call .FnI8 .Count}}`, `{{call .FnI8 1.5}}`,
		`{{call .FnStr 1}}`, `{{call .FnStr .Title}}`, `{{call .FnStr .Slug}}`, `{{call .FnStr "x"}}`,
		`{{call .FnUser .Owner}}`, `{{call .FnUser .Author}}`, `{{call .FnUser nil}}`, `{{call .FnAny 1}}`,
		`{{.Title | call .FnStr}}`, `{{.Count | call .FnStr}}`, `{{call .FnVar "a" 1 2}}`, `{{call .FnVar "a" "b"}}`,
		`{{call .FnVal .Author}}`, `{{call .FnUser .Users.missing}}`,

		// index converts a map key as call does; a slice, array or string
		// index must be an integer.
		`{{index .ByNum "a"}}`, `{{index .ByNum 1}}`, `{{index .ByNum .Small}}`, `{{index .ByNum 1.5}}`,
		`{{index .BySlug "a"}}`, `{{index .BySlug .Slug}}`, `{{index .Meta 1}}`, `{{index .Meta "anything"}}`,
		`{{index .Cards "a"}}`, `{{index .Cards 1.0}}`, `{{index .Cards .Small}}`, `{{index .Cards 0}}`,
		`{{index .Title 0}}`, `{{index .Title "a"}}`, `{{index .Fixed .Title}}`, `{{index .Users.missing 0}}`,
		`{{index .ByPtr .Users.missing}}`, `{{index .ByNum 0 0}}`, `{{index .Nested 1 "a"}}`, `{{index .Nested 1 1}}`,

		// slice of a string, a slice or an addressable array, by integers.
		`{{slice .Count}}`, `{{slice .Title 1}}`, `{{slice .Cards 0}}`, `{{slice .Owner}}`, `{{slice .Author}}`,
		`{{slice .Title 0 1 1}}`, `{{slice .Cards 0 1 1}}`, `{{slice .Cards 0 0 0 0}}`, `{{slice .Cards "a"}}`,
		`{{slice .Title 1.5}}`, `{{slice .Fixed 0}}`, `{{slice .PFix 0}}`, `{{slice .PS 0}}`, `{{slice .Cards .Small}}`,
		`{{range .Arrs}}{{slice . 0}}{{end}}`, `{{slice (index .Arrs 0) 0}}`, `{{slice .ArrMap.a 0}}`,
		`{{(slice .Title 1).Nope}}`, `{{slice 1}}`, `{{slice "abc" 1}}`,
	)
	add(argsPtr,
		`{{ptrUser .Owner}}`, `{{stringer .PStamp}}`, `{{.Ptr .Owner}}`, `{{call .FnUser .Owner}}`,
		`{{slice .Fixed 0}}`, `{{range .Users}}{{ptrUser .}}{{end}}`, `{{cardsf .Fixed}}`,
	)

	at := regexp.MustCompile(`t\.html:(\d+):(\d+)`)
	for _, tc := range cases {
		data := tc.data()
		t.Run(data.Type().String()+" "+tc.src, func(t *testing.T) {
			set := template.Must(template.New("t.html").Funcs(funcs).Parse(tc.src))
			c := checker(t, map[string]string{"t.html": tc.src}, funcs)
			findings := c.Check("t.html", Dot{Type: data.Type()})
			execErr := set.Execute(io.Discard, tc.data().Interface())
			if (len(findings) > 0) != (execErr != nil) {
				t.Fatalf("checker found %+v; execution error: %v", findings, execErr)
			}
			// text/template stops at its first error; the first finding must
			// point where it does: "template: t.html:1:8: executing ...".
			if execErr != nil {
				pos := at.FindStringSubmatch(execErr.Error())
				if pos == nil {
					t.Fatalf("no position in %v", execErr)
				}
				if want := pos[1] + ":" + pos[2]; strconv.Itoa(findings[0].Line)+":"+strconv.Itoa(findings[0].Col) != want {
					t.Errorf("first finding at %d:%d, text/template at %s (%v)", findings[0].Line, findings[0].Col, want, execErr)
				}
			}
		})
	}
}

// argData holds what the argument-type cases pass: values of named, pointer,
// array, map and function types, every map key read present but "missing".
type argData struct {
	Count  int
	Small  int8
	Ratio  float64
	Flag   bool
	Title  string
	Slug   slug
	Owner  user
	Author *user
	Cards  []card
	Fixed  [2]card
	PFix   *[2]card
	PS     *[]card
	Arrs   [][1]int
	ArrMap map[string][1]int
	Stamp  stamp
	PStamp pstamp
	PSPtr  *pstamp
	Users  map[string]user
	ByNum  map[int]string
	BySlug map[slug]string
	ByPtr  map[*user]string
	Meta   map[string]string
	Nested map[int]map[string]int
	Any    any // any: a value the checker must not look into
	F      reflect.Value
	Fn64   func(float64) float64
	FnI8   func(int8) int8
	FnStr  func(string) string
	FnUser func(*user) string
	FnVal  func(user) string
	FnAny  func(any) any // any: a parameter call fills with whatever it is given
	FnVar  func(string, ...int) string
}

func (argData) Take(n int8) int8                 { return n }
func (argData) Name(s string) string             { return s }
func (argData) Many(sep string, n ...int) string { return sep }
func (argData) Ptr(u *user) string               { return "p" }

type stamp struct{}

func (stamp) String() string { return "stamp" }

type pstamp struct{}

func (*pstamp) String() string { return "pstamp" }

func filledArgs() argData {
	cards := []card{{Name: "c"}, {Name: "d"}}
	fixed := [2]card{{Name: "a"}, {Name: "b"}}
	return argData{
		Count: 2, Small: 1, Ratio: 1.5, Flag: true, Title: "title", Slug: "s",
		Owner: user{Name: "o"}, Author: &user{Name: "a"}, Cards: cards, Fixed: fixed, PFix: &fixed, PS: &cards,
		Arrs: [][1]int{{1}}, ArrMap: map[string][1]int{"a": {1}}, PSPtr: &pstamp{},
		Users: map[string]user{"u": {Name: "u"}}, ByNum: map[int]string{0: "z", 1: "a"},
		BySlug: map[slug]string{"s": "x"}, ByPtr: map[*user]string{}, Meta: map[string]string{"anything": "m"},
		Nested: map[int]map[string]int{1: {"a": 1}}, Any: "x", F: reflect.ValueOf("f"),
		Fn64: func(f float64) float64 { return f }, FnI8: func(n int8) int8 { return n },
		FnStr: func(s string) string { return s }, FnUser: func(*user) string { return "u" },
		FnVal: func(u user) string { return u.Name }, FnAny: func(v any) any { return v }, // any: FnAny's own type
		FnVar: func(s string, _ ...int) string { return s },
	}
}

// numeric has methods whose parameters a number argument is converted to.
type numeric struct{}

func (numeric) U(n uint64) uint64   { return n }
func (numeric) F(n float64) float64 { return n }
func (numeric) I8(n int8) int8      { return n }
