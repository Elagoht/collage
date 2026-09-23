package core

import (
	"testing"

	"github.com/Elagoht/collage/internal/csrf"
	"github.com/Elagoht/collage/internal/render"
)

// The template function renders a field with one name and the verifier reads
// another. They live in packages that deliberately do not import each other, so
// nothing but this test stops them drifting apart — and a drift here is a form that
// always fails to submit, with a 403 and nothing to explain it.
func TestCSRFFieldNamesAgree(t *testing.T) {
	if render.CSRFFieldName != csrf.DefaultFieldName {
		t.Fatalf("the rendered field is %q and the verifier reads %q",
			render.CSRFFieldName, csrf.DefaultFieldName)
	}
}
