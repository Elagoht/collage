package typecheck

import (
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
	funcs := template.FuncMap{"pick": pick, "upper": strings.ToUpper}
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

		// Positions on later lines, and inside actions that span lines.
		"x\n{{range .Cards}}\n  {{.Nam}}{{end}}", "\n{{(index .ByCol\n  \"a\").Owner.Edit}}",
		"\n{{upper\n \"a\" \"b\"}}", "\n\n{{range $i, $v :=\n  .Count}}{{end}}", "{{len\n\n .Owner}}",
	)
	add(boardPtr, `{{range .Fixed}}{{.Owner.Edit}}{{end}}`, `{{(index .Fixed 0).Owner.Edit}}`, `{{.Owner.Edit}}`)

	// A *T method reached through an embedded pointer, or hidden on an
	// unaddressable value whose key or promoted field has its name.
	add(func() any { return page{&layout{U: user{Name: "u"}}} }, `{{.U.Edit}}`, `{{.U.Nope}}`, `{{.U.Name}}`) // any: case data
	add(func() any { return pm{"Foo": "x"} }, `{{.Foo}}`, `{{.Foo.Nope}}`)                                    // any: case data
	add(func() any { m := pm{"Foo": "x"}; return &m }, `{{.Foo}}`)                                            // any: case data
	add(func() any { return outer{inner{Edit: "e"}} }, `{{.Edit}}`, `{{.Edit "x"}}`)                          // any: case data
	add(func() any { return &outer{inner{Edit: "e"}} }, `{{.Edit}}`, `{{.Edit "x"}}`)                         // any: case data

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
