package types

import (
	"errors"
	"fmt"
)

// ErrTemplateType is what every TemplateTypeError matches with errors.Is.
var ErrTemplateType = errors.New("collage: template does not fit its data")

// TemplateTypeError is one expression in a fragment's template certain to fail
// when it renders, found at registration by checking the template against the
// Go type of the fragment's data.
type TemplateTypeError struct {
	Page, Fragment string
	// Template is the file the expression is in — the fragment's own, or a
	// partial it includes — or `inline template of fragment "x"`.
	Template  string
	Line, Col int
	// Expr is the expression as written, "{{.Titel}}".
	Expr string
	// Reason says what is wrong; Suggestion is the closest name that exists, or "".
	Reason, Suggestion string
}

func (e *TemplateTypeError) Error() string {
	msg := fmt.Sprintf("collage: page %q: fragment %q (%s:%d:%d): %s: %s",
		e.Page, e.Fragment, e.Template, e.Line, e.Col, e.Expr, e.Reason)
	if e.Suggestion != "" {
		msg += " (did you mean " + e.Suggestion + "?)"
	}
	return msg
}

func (e *TemplateTypeError) Unwrap() error { return ErrTemplateType }
