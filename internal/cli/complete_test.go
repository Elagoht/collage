package cli

import (
	"flag"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/plugin"
)

// Every command collage lists completes: a new one cannot ship without saying
// what its flags and arguments are.
func TestCommandSpecs_CoverEveryBuiltin(t *testing.T) {
	for _, b := range builtins {
		if _, ok := commandSpecs[b.name]; !ok {
			t.Errorf("builtin %q has no command spec", b.name)
		}
	}
}

// splitPositional reads which flags take a value from hand-kept maps; they
// must say what the flags' own declarations say.
func TestValueFlagMaps_MatchDeclarations(t *testing.T) {
	for path, want := range map[string]map[string]bool{
		"new":        newValueFlags,
		"add":        addValueFlags,
		"plugin add": pluginValueFlags,
		"plugin del": pluginValueFlags,
	} {
		got := map[string]bool{}
		specFlags(path).VisitAll(func(f *flag.Flag) {
			if !isBoolFlag(f) {
				got[f.Name] = true
			}
		})
		if !maps.Equal(got, want) {
			t.Errorf("%s: value flags declared %v, map says %v", path, slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
		}
	}
}

func TestComplete(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		want  []string
		files bool
	}{
		{[]string{""}, []string{"new", "add", "plugin", "ai", "dev", "build", "export", "serve", "routes", "inspect", "check", "completion", "man", "version", "help"}, false},
		{[]string{"ch"}, []string{"check"}, false},
		{[]string{"add", ""}, addKinds, false},
		{[]string{"add", "pa"}, []string{"page"}, false},
		{[]string{"add", "--file", "page", ""}, nil, false},
		{[]string{"add", "page", "x", "--la"}, []string{"--layout"}, false},
		{[]string{"add", "page", "x", "-fi"}, []string{"-file"}, false},
		{[]string{"plugin", ""}, []string{"add", "del", "check"}, false},
		{[]string{"plugin", "add", "@f"}, []string{"@forms"}, false},
		{[]string{"plugin", "add", "elagoht/fl"}, []string{"elagoht/flash"}, false},
		{[]string{"plugin", "add", "-dir", ""}, nil, true},
		{[]string{"plugin", "check", ""}, nil, true},
		{[]string{"ai", ""}, []string{"init"}, false},
		{[]string{"ai", "init", "--"}, []string{"--dir"}, false},
		{[]string{"new", "site", "--template", ""}, []string{"minimal", "demo"}, false},
		{[]string{"new", "site", "--template=de"}, []string{"--template=demo"}, false},
		{[]string{"new", "site", "--template", "=", "de"}, []string{"demo"}, false},
		{[]string{"new", "site", "--with", "@forms,@b"}, []string{"@forms,@blog"}, false},
		{[]string{"new", "site", "--dir", ""}, nil, true},
		{[]string{"new", ""}, nil, false},
		{[]string{"build", "-o", ""}, nil, true},
		{[]string{"build", "-os", "lin"}, []string{"linux"}, false},
		{[]string{"export", "-out", ""}, nil, true},
		{[]string{"serve", "-dir", "d"}, nil, true},
		{[]string{"help", "pl"}, []string{"plugin"}, false},
		{[]string{"completion", ""}, []string{"bash", "zsh", "fish"}, false},
		{[]string{"check", "-"}, []string{"-json"}, false},
		{[]string{"frobnicate", ""}, nil, false},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			got, files := complete(tc.args)
			if !slices.Equal(got, tc.want) || files != tc.files {
				t.Errorf("complete(%q) = %q, files %v; want %q, files %v", tc.args, got, files, tc.want, tc.files)
			}
		})
	}
}

// The hidden command the shell scripts call prints one candidate a line, then
// ":files" when the shell should complete paths, and never fails.
func TestRun_Complete(t *testing.T) {
	c, out, errOut := testCLI()
	if code := c.Run(t.Context(), []string{"__complete", "build", "-o", ""}); code != 0 || out.String() != ":files\n" || errOut.Len() != 0 {
		t.Errorf("__complete build -o '' = %d, %q, %q", code, out, errOut)
	}
	c, out, _ = testCLI()
	if code := c.Run(t.Context(), []string{"__complete", "add", "pa"}); code != 0 || out.String() != "page\n" {
		t.Errorf("__complete add pa = %d, %q", code, out)
	}
	c, out, errOut = testCLI()
	if code := c.Run(t.Context(), []string{"__complete"}); code != 0 || errOut.Len() != 0 {
		t.Errorf("__complete with nothing = %d, %q, %q", code, out, errOut)
	}
}

// __complete is reserved, but neither listed nor suggested.
func TestComplete_HiddenButReserved(t *testing.T) {
	c, _, errOut := testCLI()
	c.Commands = []plugin.Command{{Name: "__complete", Short: "x", Usage: "x"}}
	if code := c.Run(t.Context(), []string{"version"}); code != 2 || !strings.Contains(errOut.String(), "built-in") {
		t.Errorf("a plugin command named __complete = %d, %q; want refused", code, errOut)
	}
	c, out, _ := testCLI()
	c.Run(t.Context(), []string{"help"})
	if strings.Contains(out.String(), "__complete") {
		t.Errorf("help lists __complete")
	}
}
