package render

import (
	"strings"
	"testing"
)

// The marker {{hoist}} writes is an HTML comment returned as safe markup, so an
// area name that carries "-->" closes the comment and what follows is live HTML:
// {{hoist .Area}} with a name from a request became a script injection.
func TestHoist_RefusesAnAreaNameThatCouldCloseTheMarker(t *testing.T) {
	hoist := hoistFunc("tok")

	for _, area := range []string{"x--><script>alert(1)</script>", "a b", "head\n", "é", ""} {
		got, err := hoist(area)
		if err == nil {
			t.Errorf("hoist(%q) = %q, want an error", area, got)
		}
		if strings.Contains(string(got), "<script>") {
			t.Errorf("hoist(%q) wrote %q", area, got)
		}
	}
	for _, area := range []string{"head", "foot-er", "scripts_2", "A9"} {
		if got, err := hoist(area); err != nil || !strings.Contains(string(got), area) {
			t.Errorf("hoist(%q) = %q, %v; want a marker", area, got, err)
		}
	}
}
