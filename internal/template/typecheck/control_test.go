package typecheck

import (
	"iter"
	"reflect"
	"strings"
	"testing"
)

type board struct {
	Cards []card
	Fixed [2]card
	ByCol map[string]card
	Count int
	Seq   iter.Seq[card]
	Title string
	Owner user
	Ch    chan card
	Seq2  iter.Seq2[card, int]
	PS    *[]card
	PFix  *[2]card
}

type card struct {
	Name  string
	Owner user
}

var boardType = reflect.TypeFor[board]()

func TestCheck_Control(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"range slice", `{{range .Cards}}{{.Name}}{{end}}`, nil},
		{"range slice wrong field", `{{range .Cards}}{{.Nam}}{{end}}`,
			[]string{"t.html:1 type typecheck.card has no field or method Nam [Name]"}},
		{"range else keeps outer dot", `{{range .Cards}}{{else}}{{.Title}}{{end}}`, nil},
		{"range two variables", `{{range $i, $c := .Cards}}{{$c.Name}}{{$i}}{{end}}`, nil},
		{"range map", `{{range $k, $v := .ByCol}}{{$v.Name}}{{end}}`, nil},
		{"range int", `{{range .Count}}{{.}}{{end}}`, nil},
		{"range int two variables", `{{range $i, $v := .Count}}{{end}}`,
			[]string{"t.html:1 range over type int cannot declare two variables"}},
		{"range iter.Seq", `{{range .Seq}}{{.Name}}{{end}}`, nil},
		{"range string", `{{range .Title}}{{end}}`,
			[]string{"t.html:1 range cannot iterate over type string"}},
		{"range struct", `{{range .Owner}}{{end}}`,
			[]string{"t.html:1 range cannot iterate over type typecheck.user"}},
		{"slice element is addressable", `{{range .Cards}}{{.Owner.Edit}}{{end}}`, nil},
		{"map element is not addressable", `{{range .ByCol}}{{.Owner.Edit}}{{end}}`,
			[]string{"t.html:1 method Edit has a pointer receiver, and this typecheck.user is not addressable: pass the data as a pointer, or reach the value through a slice"}},
		{"with narrows dot", `{{with .Owner}}{{.Name}}{{end}}`, nil},
		{"with narrows wrong field", `{{with .Owner}}{{.Title}}{{end}}`,
			[]string{"t.html:1 type typecheck.user has no field or method Title"}},
		{"with else keeps outer dot", `{{with .Owner}}{{else}}{{.Title}}{{end}}`, nil},
		{"if walks both branches", `{{if .Title}}{{.Nope}}{{else}}{{.Nada}}{{end}}`,
			[]string{"t.html:1 type typecheck.board has no field or method Nope", "t.html:1 type typecheck.board has no field or method Nada"}},
		{"variable assigned in a branch is uncertain", `{{$x := .Owner}}{{if .Title}}{{$x = .Title}}{{end}}{{$x.Name}}`, nil},
		{"variable assigned later in a range body is uncertain", `{{$x := .Title}}{{range .Cards}}{{$x.Name}}{{$x = .Owner}}{{end}}`, nil},
		{"range chan two variables", `{{range $i, $c := .Ch}}{{$c.Name}}{{$i}}{{end}}`, nil},
		{"range Seq2 no variable", `{{range .Seq2}}{{.Name}}{{end}}`, nil},
		{"range Seq2 one variable", `{{range $k := .Seq2}}{{$k.Name}}{{end}}`, nil},
		{"range Seq2 two variables", `{{range $k, $v := .Seq2}}{{$k.Name}}{{$v}}{{end}}`, nil},
		{"range assigns both variables", `{{$i := 0}}{{$x := .Title}}{{range $i, $x = .Cards}}{{end}}{{$x.Name}}`, nil},
		{"array by value is not addressable", `{{range .Fixed}}{{.Owner.Edit}}{{end}}`,
			[]string{"t.html:1 method Edit has a pointer receiver, and this typecheck.user is not addressable: pass the data as a pointer, or reach the value through a slice"}},
		{"array by pointer is addressable", `{{range .PFix}}{{.Owner.Edit}}{{end}}`, nil},
		{"pointer to slice", `{{range .PS}}{{.Owner.Edit}}{{.Name}}{{end}}`, nil},
		{"variable in scope", `{{$o := .Owner}}{{$o.Name}}`, nil},
		{"variable wrong field", `{{$o := .Owner}}{{$o.Nme}}`,
			[]string{"t.html:1 type typecheck.user has no field or method Nme [Name]"}},
		{"root variable", `{{range .Cards}}{{$.Title}}{{end}}`, nil},
		{"variable out of scope after end", `{{with .Owner}}{{$x := .Name}}{{end}}{{$y := 1}}`, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := reasons(t, test.src, boardType)
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Errorf("Check(%q)\n got: %q\nwant: %q", test.src, got, test.want)
			}
		})
	}
}

func TestCheck_Partials(t *testing.T) {
	files := map[string]string{
		"page.html": `{{template "card.html" .Owner}}{{range .Cards}}{{template "card.html" .}}{{end}}`,
		"card.html": `<b>{{.Name}}</b>`,
	}
	c := checker(t, files, nil)
	if got := c.Check("page.html", Dot{Type: boardType}); len(got) != 0 {
		t.Errorf("partial fitting both types: %+v, want none", got)
	}

	files["card.html"] = `<b>{{.Owner.Name}}</b>`
	c = checker(t, files, nil)
	got := c.Check("page.html", Dot{Type: boardType})
	if len(got) != 1 || got[0].Template != "card.html" || got[0].Line != 1 || !strings.Contains(got[0].Reason, "type typecheck.user has no field or method Owner") {
		t.Errorf("partial misfitting one type: %+v, want one finding in card.html about typecheck.user", got)
	}
}

func TestCheck_PartialWithoutPipeIsUnknown(t *testing.T) {
	c := checker(t, map[string]string{"page.html": `{{template "p.html"}}`, "p.html": `{{.Anything}}`}, nil)
	if got := c.Check("page.html", Dot{Type: boardType}); len(got) != 0 {
		t.Errorf("partial with no data: %+v, want none", got)
	}
}

func TestCheck_DefineBlocks(t *testing.T) {
	c := checker(t, map[string]string{"page.html": `{{define "row"}}{{.Nme}}{{end}}{{range .Cards}}{{template "row" .}}{{end}}`}, nil)
	got := c.Check("page.html", Dot{Type: boardType})
	if len(got) != 1 || got[0].Template != "page.html" || !strings.Contains(got[0].Reason, "typecheck.card has no field or method Nme") {
		t.Errorf("define: %+v, want one finding in page.html", got)
	}
}
