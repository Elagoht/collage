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
	hidden   string
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
