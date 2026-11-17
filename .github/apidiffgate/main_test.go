package main

import (
	"slices"
	"testing"
)

func TestLeadingIdent(t *testing.T) {
	for line, want := range map[string]string{
		"- Key.Name: removed":                      "Key",
		"- (*App).RegisterPlugin: changed from a":  "App",
		"- Metrics.Foo: added":                     "Metrics",
		"- Key[T].Name: removed":                   "Key",
		"- ActionResult: changed from struct to x": "ActionResult",
		"Incompatible changes:":                    "",
	} {
		if got := leadingIdent(line); got != want {
			t.Errorf("leadingIdent(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestMethodAddedRE(t *testing.T) {
	for line, want := range map[string]bool{
		"- Data.New: added":           true,
		"- (*T).New: added":           true,
		"- Key[T].New: added":         true,
		"- Key[T].New: removed":       false,
		"- Data: changed from a to b": false,
	} {
		if got := methodAddedRE.MatchString(line); got != want {
			t.Errorf("methodAddedRE(%q) = %v, want %v", line, got, want)
		}
	}
}

func TestFilter_SealedGrowthIsNotCounted(t *testing.T) {
	g := growth{
		sealed:   map[string]bool{"Host": true, "Data": true},
		grewOnly: map[string]bool{"Host": true},
	}
	lines := []string{
		"- Host: changed from interface{A()} to interface{A(); B()}",
		"- Data.New: added",
		"- Plugin: changed from interface{Init(Host)} to interface{Init(Host)}",
		"- Metrics.New: added",
		"- Key.Name: removed",
	}
	kept, exempt := filter(lines, g, sealedGrowthIn(lines, g))
	if want := []string{"- Metrics.New: added", "- Key.Name: removed"}; !slices.Equal(kept, want) {
		t.Errorf("kept = %v, want %v", kept, want)
	}
	if len(exempt) != 3 {
		t.Errorf("exempt = %v, want the Host growth, Data.New and the Plugin fallout", exempt)
	}
}

func TestFilter_IdenticalTextWithoutSealedGrowthFails(t *testing.T) {
	// Nothing sealed grew, so a change the report cannot show is not explained.
	lines := []string{"- Plugin: changed from interface{Init(Host)} to interface{Init(Host)}"}
	kept, exempt := filter(lines, growth{sealed: map[string]bool{"Host": true}, grewOnly: map[string]bool{}}, false)
	if len(kept) != 1 || len(exempt) != 0 {
		t.Errorf("kept = %v, exempt = %v, want the line kept", kept, exempt)
	}
}

func TestFilter_ASealedInterfaceThatLostAMethodFails(t *testing.T) {
	g := growth{sealed: map[string]bool{"Host": true}, grewOnly: map[string]bool{}}
	kept, _ := filter([]string{"- Host: changed from interface{A(); B()} to interface{A()}"}, g, false)
	if len(kept) != 1 {
		t.Errorf("kept = %v, want the removal counted", kept)
	}
}

func TestSealedGrowth_OnlyAdditionsAreGrowth(t *testing.T) {
	old := &surface{literals: map[string]map[string]string{"Host": {"A": "func()", "B": "func() int"}}, sealed: map[string]bool{"Host": true}}
	grew := &surface{literals: map[string]map[string]string{"Host": {"A": "func()", "B": "func() int", "C": "func()"}}}
	changed := &surface{literals: map[string]map[string]string{"Host": {"A": "func()", "B": "func() string"}}}
	removed := &surface{literals: map[string]map[string]string{"Host": {"A": "func()"}}}
	for name, c := range map[string]struct {
		now  *surface
		want bool
	}{"grew": {grew, true}, "changed": {changed, false}, "removed": {removed, false}} {
		if got := sealedGrowth(old, c.now).grewOnly["Host"]; got != c.want {
			t.Errorf("%s: grewOnly = %v, want %v", name, got, c.want)
		}
	}
}
