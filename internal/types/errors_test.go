package types

import (
	"errors"
	"fmt"
	"testing"
)

// TestErrNotFound_Exists confirms the sentinel exists, carries the house message
// convention, and is distinct from every other sentinel in this package: a
// DataHandler that wraps a different sentinel must not be mistaken for ErrNotFound,
// and ErrNotFound itself must not satisfy errors.Is against any of the others.
func TestErrNotFound_Exists(t *testing.T) {
	if ErrNotFound == nil {
		t.Fatal("ErrNotFound must not be nil")
	}
	if got, want := ErrNotFound.Error(), "collage: not found"; got != want {
		t.Errorf("ErrNotFound.Error() = %q, want %q", got, want)
	}

	others := []error{
		ErrNilFragment,
		ErrUnknownSlot,
		ErrSlotOccupied,
		ErrFragmentCycle,
		ErrMissingContent,
		ErrMissingTTL,
		ErrInvalidTimeout,
		ErrInvalidTTL,
		ErrRequiredSlotUnfilled,
		ErrInvalidSlotDefinition,
		ErrInvalidRedirectStatus,
		ErrSelfErrorPage,
		ErrEmptyName,
		ErrEmptyTemplatePath,
		ErrInvalidPath,
	}
	for _, other := range others {
		if errors.Is(ErrNotFound, other) {
			t.Errorf("ErrNotFound must not be errors.Is to %v", other)
		}
		if errors.Is(other, ErrNotFound) {
			t.Errorf("%v must not be errors.Is to ErrNotFound", other)
		}
	}
}

// TestErrNotFound_WrappedIsDetectable confirms a DataHandler-style wrap of
// ErrNotFound is still detectable with errors.Is, which is the whole mechanism this
// sentinel exists to support.
func TestErrNotFound_WrappedIsDetectable(t *testing.T) {
	wrapped := fmt.Errorf("blog: slug %q: %w", "no-such-slug", ErrNotFound)
	if !errors.Is(wrapped, ErrNotFound) {
		t.Error("errors.Is must detect ErrNotFound through a wrap")
	}
}
