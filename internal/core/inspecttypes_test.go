package core

import (
	"reflect"
	"testing"
	"time"
)

type itUser struct{ Name string }
type itComment struct {
	Body    string
	Replies []itComment
	By      *itUser
}
type itPost struct {
	Title    string
	At       time.Time
	Comments []itComment
	//lint:ignore U1000 an unexported field is what the table must leave out.
	hidden string
}

func (p itPost) URL() string              { return "/" }
func (p *itPost) Edit(a, b string) string { return a + b }

func TestTypeTable(t *testing.T) {
	table := typeTable([]reflect.Type{reflect.TypeFor[*itPost]()})
	post, ok := table["core.itPost"]
	if !ok {
		t.Fatalf("table = %v, want core.itPost", table)
	}
	if post.Kind != "struct" || len(post.Fields) != 3 {
		t.Errorf("itPost = %+v, want struct with Title, At, Comments", post)
	}
	if post.Fields[2].Type != "[]core.itComment" {
		t.Errorf("Comments field = %+v", post.Fields[2])
	}
	if len(post.Methods) != 2 || post.Methods[0].Name != "Edit" || post.Methods[0].Args != 2 || post.Methods[1].Returns != "string" {
		t.Errorf("itPost methods = %+v, want Edit(2) and URL", post.Methods)
	}
	if _, ok := table["core.itComment"]; !ok {
		t.Error("recursive itComment missing")
	}
	if _, ok := table["core.itUser"]; !ok {
		t.Error("itUser, reached through a pointer field, missing")
	}
	if _, ok := table["time.Time"]; ok {
		t.Error("time.Time is a standard library type and must stay opaque")
	}
}

func TestIsStandard(t *testing.T) {
	if !isStandard(reflect.TypeFor[time.Time]()) {
		t.Error("time.Time is standard")
	}
	if isStandard(reflect.TypeFor[itPost]()) {
		t.Error("a type of this module is not standard")
	}
}

type itBase struct{ ID int }

func (itBase) Label() string { return "" }

type itChildX struct {
	itBase
	Name string
}
type itPosts []itComment

func (p itPosts) Count() int { return len(p) }

type itMapped struct {
	M map[itUser]itComment
	C chan itBase
}

func TestTypeTableShapes(t *testing.T) {
	// promoted fields and methods; an unexported embedded struct is not a field
	tb := typeTable([]reflect.Type{reflect.TypeFor[itChildX]()})
	c := tb["core.itChildX"]
	if len(c.Fields) != 2 || c.Fields[0].Name != "ID" || len(c.Methods) != 1 || c.Methods[0].Name != "Label" {
		t.Errorf("itChildX = %+v", c)
	}
	// map key and elem, chan elem
	tb = typeTable([]reflect.Type{reflect.TypeFor[itMapped]()})
	for _, k := range []string{"core.itUser", "core.itComment", "core.itBase"} {
		if _, ok := tb[k]; !ok {
			t.Errorf("%s missing from %v", k, tb)
		}
	}
	// named slice with a method keeps its own entry
	tb = typeTable([]reflect.Type{reflect.TypeFor[itPosts]()})
	if e, ok := tb["core.itPosts"]; !ok || e.Kind != "slice" || len(e.Methods) != 1 || e.Methods[0].Name != "Count" {
		t.Errorf("itPosts = %+v (%v)", e, ok)
	}
	if _, ok := tb["core.itComment"]; !ok {
		t.Error("element of a named slice missing")
	}
	// anonymous struct root
	anon := reflect.TypeFor[struct{ Posts []itUser }]()
	tb = typeTable([]reflect.Type{anon})
	if e, ok := tb[anon.String()]; !ok || len(e.Fields) != 1 {
		t.Errorf("anonymous root = %+v (%v)", e, ok)
	}
	if _, ok := tb["core.itUser"]; !ok {
		t.Error("named type under an anonymous struct missing")
	}
	// anonymous struct nested in a slice
	tb = typeTable([]reflect.Type{reflect.TypeFor[[]struct{ By itUser }]()})
	if _, ok := tb["core.itUser"]; !ok {
		t.Errorf("named type under []struct missing: %v", tb)
	}
}

type ItEmbedded struct{ Code string }

func (ItEmbedded) Tag() string { return "" }

type itWithEmbedded struct {
	ItEmbedded
	*itUser
	Name string
}

func TestTypeTableEmbedded(t *testing.T) {
	tb := typeTable([]reflect.Type{reflect.TypeFor[itWithEmbedded]()})
	e := tb["core.itWithEmbedded"]
	byName := map[string]InspectedField{}
	for _, f := range e.Fields {
		byName[f.Name] = f
	}
	if f, ok := byName["ItEmbedded"]; !ok || !f.Embedded || f.Type != "core.ItEmbedded" {
		t.Errorf("embedded field = %+v (%v), want ItEmbedded core.ItEmbedded embedded", f, ok)
	}
	if f, ok := byName["Code"]; !ok || f.Embedded {
		t.Errorf("promoted Code = %+v (%v), want a plain field", f, ok)
	}
	if f, ok := byName["Name"]; !ok || f.Embedded {
		t.Errorf("promoted Name through *itUser = %+v (%v)", f, ok)
	}
	if _, ok := byName["itUser"]; ok {
		t.Error("an unexported embedded field is not a field a template can reach")
	}
	if _, ok := tb["core.ItEmbedded"]; !ok {
		t.Error("the embedded type is not listed")
	}
}

type itSlice []itUser
type itArray [2]itUser
type itMap map[string]itComment
type itPtr *itUser
type itChan chan itBase

func TestTypeTableContainerElems(t *testing.T) {
	tb := typeTable([]reflect.Type{
		reflect.TypeFor[itSlice](), reflect.TypeFor[itArray](), reflect.TypeFor[itMap](),
		reflect.TypeFor[itPtr](), reflect.TypeFor[itChan](),
	})
	for name, want := range map[string]InspectedType{
		"core.itSlice": {Kind: "slice", Elem: "core.itUser"},
		"core.itArray": {Kind: "array", Elem: "core.itUser"},
		"core.itMap":   {Kind: "map", Key: "string", Elem: "core.itComment"},
		"core.itPtr":   {Kind: "ptr", Elem: "core.itUser"},
		"core.itChan":  {Kind: "chan", Elem: "core.itBase"},
	} {
		got := tb[name]
		if got.Kind != want.Kind || got.Elem != want.Elem || got.Key != want.Key {
			t.Errorf("%s = %+v, want %+v", name, got, want)
		}
	}
	if s := tb["core.itUser"]; s.Elem != "" || s.Key != "" {
		t.Errorf("a struct has no elem or key: %+v", s)
	}
}
