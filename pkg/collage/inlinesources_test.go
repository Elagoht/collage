package collage_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

// The cap on inline sources a slot resolver may add fails a fragment with an
// error an application can name: it was in the changelog and the docs, but
// lived in an internal package nothing outside the module could import.
func TestErrTooManyInlineSources_IsNameable(t *testing.T) {
	wrapped := fmt.Errorf("fragment %q: %w", "card", collage.ErrTooManyInlineSources)
	if !errors.Is(wrapped, collage.ErrTooManyInlineSources) {
		t.Fatal("a wrapped ErrTooManyInlineSources is not recognised by errors.Is")
	}
}
