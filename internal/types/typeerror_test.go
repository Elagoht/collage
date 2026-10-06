package types

import (
	"errors"
	"testing"
)

func TestTemplateTypeError_Message(t *testing.T) {
	err := &TemplateTypeError{
		Page: "post", Fragment: "post-body", Template: "templates/post.html", Line: 12, Col: 9,
		Expr: "{{.Titel}}", Reason: "type blog.Post has no field or method Titel", Suggestion: "Title",
	}
	want := `collage: page "post": fragment "post-body" (templates/post.html:12:9): {{.Titel}}: type blog.Post has no field or method Titel (did you mean Title?)`
	if err.Error() != want {
		t.Errorf("Error() =\n%s\nwant\n%s", err.Error(), want)
	}
	if !errors.Is(err, ErrTemplateType) {
		t.Error("errors.Is(err, ErrTemplateType) = false")
	}
	err.Suggestion = ""
	if got := err.Error(); got[len(got)-1] == ')' {
		t.Errorf("no suggestion, yet the message ends with one: %s", got)
	}
}
