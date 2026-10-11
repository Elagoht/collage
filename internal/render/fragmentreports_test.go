package render

import (
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// A plugin may change the reports it is handed; the render's own metadata is
// not theirs to change.
func TestFragmentReports_IsACopy(t *testing.T) {
	result := &Result{Metadata: &Metadata{Fragments: []types.FragmentReport{{Name: "a"}, {Name: "b", Failed: true}}}}

	reports := FragmentReports(result)
	reports[0].Name = "changed"
	reports[1].Failed = false

	if got := result.Metadata.Fragments[0].Name; got != "a" {
		t.Errorf("Metadata.Fragments[0].Name = %q after the copy was changed, want %q", got, "a")
	}
	if !result.Metadata.Fragments[1].Failed {
		t.Error("Metadata.Fragments[1].Failed = false after the copy was changed, want true")
	}
}

func TestFragmentReports_NilSafe(t *testing.T) {
	for name, result := range map[string]*Result{
		"nil result":   nil,
		"nil metadata": {},
	} {
		if got := FragmentReports(result); got != nil {
			t.Errorf("%s: FragmentReports = %v, want nil", name, got)
		}
	}
}
