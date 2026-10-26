package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/plugin"
)

// firstLine is s up to its first newline.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// A name the CLI knows the valid set of, mistyped, is answered with the
// closest valid one on the error's own line; a name close to nothing keeps the
// line as it was.
func TestRun_DidYouMean(t *testing.T) {
	kinds := strings.Join(addKinds, ", ")
	tests := []struct {
		name     string
		commands []plugin.Command
		args     []string
		code     int
		line     string // stderr's first line, exactly
	}{
		{"command", nil, []string{"chek"}, 2,
			`collage: unknown command: "chek" (did you mean "check"?)`},
		{"command far off", nil, []string{"frobnicate"}, 2,
			`collage: unknown command: "frobnicate"`},
		{"plugin command", []plugin.Command{{Name: "migrate", Short: "m", Usage: "migrate"}}, []string{"migrat"}, 2,
			`collage: unknown command: "migrat" (did you mean "migrate"?)`},
		{"help command", nil, []string{"help", "chek"}, 2,
			`collage: unknown command: "chek" (did you mean "check"?)`},
		{"help command far off", nil, []string{"help", "frobnicate"}, 2,
			`collage: unknown command: "frobnicate"`},
		{"plugin subcommand", nil, []string{"plugin", "ad"}, 2,
			`collage: plugin: unknown subcommand "ad" (did you mean "add"?)`},
		{"plugin subcommand far off", nil, []string{"plugin", "frobnicate"}, 2,
			`collage: plugin: unknown subcommand "frobnicate"`},
		{"ai subcommand", nil, []string{"ai", "int"}, 2,
			`collage: ai: unknown subcommand "int" (did you mean "init"?)`},
		{"ai subcommand far off", nil, []string{"ai", "frobnicate"}, 2,
			`collage: ai: unknown subcommand "frobnicate"`},
		{"add kind", nil, []string{"add", "pgae", "x"}, 2,
			`collage: add: want a kind (` + kinds + `) and a name (did you mean "page"?)`},
		{"add kind without a name", nil, []string{"add", "midleware"}, 2,
			`collage: add: want a kind (` + kinds + `) and a name (did you mean "middleware"?)`},
		{"add kind far off", nil, []string{"add", "frobnicate", "x"}, 2,
			`collage: add: want a kind (` + kinds + `) and a name`},
		{"new template", nil, []string{"new", "x", "--template", "demi"}, 2,
			`collage: unknown template: "demi"; the templates are "minimal" and "demo" (did you mean "demo"?)`},
		{"new template far off", nil, []string{"new", "x", "--template", "blog"}, 2,
			`collage: unknown template: "blog"; the templates are "minimal" and "demo"`},
		{"bundle", nil, []string{"plugin", "add", "@prd"}, 2,
			`collage: plugin add: "@prd" is not a bundle; the bundles are ` + bundleNames() + ` (did you mean "@prod"?)`},
		{"bundle far off", nil, []string{"plugin", "add", "@frobnicate"}, 2,
			`collage: plugin add: "@frobnicate" is not a bundle; the bundles are ` + bundleNames()},
		{"bundle in new --with", nil, []string{"new", "x", "--with", "@blgo"}, 2,
			`collage: new: --with: "@blgo" is not a bundle; the bundles are ` + bundleNames() + ` (did you mean "@blog"?)`},
		{"check flag", nil, []string{"check", "-jsno"}, 2,
			`collage: check takes no arguments but -json (did you mean "-json"?)`},
		{"check flag far off", nil, []string{"check", "-frobnicate"}, 2,
			`collage: check takes no arguments but -json`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, out, errOut := testCLI()
			c.Commands = tt.commands
			// new would otherwise scaffold into the working directory were
			// its arguments accepted.
			t.Chdir(t.TempDir())
			code := c.Run(context.Background(), tt.args)
			if code != tt.code {
				t.Errorf("Run(%q) = %d, want %d\n%s", tt.args, code, tt.code, errOut)
			}
			if got := firstLine(errOut.String()); got != tt.line {
				t.Errorf("stderr's first line:\n got %s\nwant %s", got, tt.line)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want empty", out)
			}
		})
	}
}

// Every command that parses flags names the closest flag it has on the line
// the flag package writes, then its usage, as before; a flag close to none of
// them leaves the output exactly as the flag package writes it.
func TestRun_DidYouMeanFlag(t *testing.T) {
	const undefined = "flag provided but not defined: "
	tests := []struct {
		name  string
		args  []string
		line  string
		usage string
	}{
		{"new", []string{"new", "x", "--tempalte", "demo"}, `-tempalte (did you mean "-template"?)`, newUsage},
		{"new far off", []string{"new", "x", "--frobnicate"}, `-frobnicate`, newUsage},
		{"add", []string{"add", "page", "x", "--flie"}, `-flie (did you mean "-file"?)`, addUsage},
		{"build", []string{"build", "-arhc", "arm64"}, `-arhc (did you mean "-arch"?)`, buildUsage},
		{"build far off", []string{"build", "-frobnicate"}, `-frobnicate`, buildUsage},
		{"export", []string{"export", "-clen"}, `-clen (did you mean "-clean"?)`, exportUsage},
		{"serve", []string{"serve", "--prot", "80"}, `-prot (did you mean "-port"?)`, serveUsage},
		{"dev has no flags", []string{"dev", "-dri"}, `-dri`, devUsage},
		{"plugin add", []string{"plugin", "add", "-dri", "x", "acme/greet"}, `-dri (did you mean "-dir"?)`, pluginUsage()},
		{"ai init", []string{"ai", "init", "-dri", "x"}, `-dri (did you mean "-dir"?)`, aiUsage},
		{"plugin del", []string{"plugin", "del", "-dri", "x", "acme/greet"}, `-dri (did you mean "-dir"?)`, pluginUsage()},
		{"plugin check", []string{"plugin", "check", "-relase"}, `-relase (did you mean "-release"?)`, pluginUsage()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, out, errOut := testCLI()
			c.Runner = &fakeRunner{}
			t.Chdir(t.TempDir())
			if code := c.Run(context.Background(), tt.args); code != 2 {
				t.Errorf("Run(%q) = %d, want 2", tt.args, code)
			}
			if want := undefined + tt.line + "\n" + tt.usage; errOut.String() != want {
				t.Errorf("stderr:\n got %q\nwant %q", errOut, want)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want empty", out)
			}
		})
	}
}

// -h still prints the usage alone and succeeds.
func TestRun_FlagHelpUnchanged(t *testing.T) {
	c, _, errOut := testCLI()
	if code := c.Run(context.Background(), []string{"export", "-h"}); code != 0 {
		t.Fatalf("Run = %d, want 0", code)
	}
	if errOut.String() != exportUsage {
		t.Errorf("stderr = %q, want the usage alone", errOut)
	}
}

// A flag given a value it cannot take is reported as the flag package words
// it, then the usage.
func TestRun_FlagBadValueUnchanged(t *testing.T) {
	c, _, errOut := testCLI()
	if code := c.Run(context.Background(), []string{"serve", "-port", "x"}); code != 2 {
		t.Fatalf("Run = %d, want 2", code)
	}
	want := `invalid value "x" for flag -port: parse error` + "\n" + serveUsage
	if errOut.String() != want {
		t.Errorf("stderr:\n got %q\nwant %q", errOut, want)
	}
}

// A plugin go get cannot fetch, close to one the bundles name, is answered
// with that one; one close to none, or one the bundles name themselves, is
// reported as the go tool words it.
func TestPluginAdd_DidYouMean(t *testing.T) {
	needGo(t)
	withBundles(t, []bundle{{"acme", "fixtures", []string{"acme/greet", "acme/plain"}}})
	dir := pluginProject(t, multiFixtures()...)

	code, _, errOut := pluginCmd(t, dir, "add", "acme/gret")
	if code != 1 || !strings.HasSuffix(errOut, ` (did you mean "acme/greet"?)`+"\n") {
		t.Errorf("typo: %d\n%s", code, errOut)
	}
	code, _, errOut = pluginCmd(t, dir, "add", "acme/frobnicate")
	if code != 1 || strings.Contains(errOut, "did you mean") {
		t.Errorf("far off: %d\n%s", code, errOut)
	}
	withBundles(t, []bundle{{"acme", "fixtures", []string{"acme/greet", "acme/missing"}}})
	code, _, errOut = pluginCmd(t, dir, "add", "acme/missing")
	if code != 1 || strings.Contains(errOut, "did you mean") {
		t.Errorf("known but missing: %d\n%s", code, errOut)
	}
}

// The go tool's message for a module it cannot fetch runs over several lines;
// the hint goes at the end of the first.
func TestOnFirstLine(t *testing.T) {
	tests := []struct{ msg, hint, want string }{
		{"go: x: 404 Not Found\n\tserver response: not found", ` (did you mean "a/b"?)`,
			"go: x: 404 Not Found (did you mean \"a/b\"?)\n\tserver response: not found"},
		{"go: x: 404 Not Found", ` (did you mean "a/b"?)`, `go: x: 404 Not Found (did you mean "a/b"?)`},
		{"go: x\n\ty", "", "go: x\n\ty"},
	}
	for _, tt := range tests {
		if got := onFirstLine(errors.New(tt.msg), tt.hint).Error(); got != tt.want {
			t.Errorf("onFirstLine(%q, %q) = %q, want %q", tt.msg, tt.hint, got, tt.want)
		}
	}
}

// A plugin del is given that plugins.go does not have is answered with the
// closest one it does.
func TestPluginDel_DidYouMean(t *testing.T) {
	needGo(t)
	dir := pluginProject(t, multiFixtures()...)
	if code, _, e := pluginCmd(t, dir, "add", "acme/greet"); code != 0 {
		t.Fatal(e)
	}
	code, _, errOut := pluginCmd(t, dir, "del", "acme/gret")
	if want := `collage: plugin del: acme/gret is not in plugins.go; it has: acme/greet (did you mean "acme/greet"?)` + "\n"; code != 1 || errOut != want {
		t.Errorf("typo: %d\n got %q\nwant %q", code, errOut, want)
	}
	code, _, errOut = pluginCmd(t, dir, "del", "acme/frobnicate")
	if want := "collage: plugin del: acme/frobnicate is not in plugins.go; it has: acme/greet\n"; code != 1 || errOut != want {
		t.Errorf("far off: %d\n got %q\nwant %q", code, errOut, want)
	}
}
