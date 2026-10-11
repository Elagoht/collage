package cli

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/cli/plugincontract"
)

func pluginCmd(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	c, out, errOut := testCLI()
	code := c.Run(context.Background(), append(append([]string{"plugin"}, args...), "-dir", dir))
	return code, out.String(), errOut.String()
}

func needGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not available")
	}
}

func goBuild(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
}

func TestPluginAdd(t *testing.T) {
	needGo(t)
	dir := pluginProject(t,
		fixturePlugin("acme", "greet", "v0.1.0", "func New(opts Options) *Plugin { return &Plugin{opts: opts} }", ""),
		fixturePlugin("acme", "plain", "v0.2.0", "func New() *Plugin { return &Plugin{} }", `"setup": "Pass it nothing."`),
	)

	code, out, errOut := pluginCmd(t, dir, "add", "acme/greet")
	if code != 0 {
		t.Fatalf("add = %d\n%s%s", code, out, errOut)
	}
	wantInOrder(t, out, "added acme/greet v0.1.0", "plugins.go", "greet.New(greet.Options{})",
		"plugins-config.json", `"acme/greet": {}`)
	// The minimal scaffold does not call plugins() yet (Task 7 makes it), so
	// add says how to wire it.
	wantInOrder(t, out, "plugins.go is new: give it to collage.New once", "Plugins: plugins(),")
	if code, out, errOut := pluginCmd(t, dir, "add", "acme/plain"); code != 0 || !strings.Contains(out, "Pass it nothing.") {
		t.Fatalf("add plain = %d\n%s%s", code, out, errOut)
	}
	wantInOrder(t, readFile(t, dir, "plugins.go"), `greet "github.com/acme/collage-greet"`, `plain "github.com/acme/collage-plain"`,
		"greet.New(greet.Options{}),", "plain.New(),")
	wantInOrder(t, readFile(t, dir, "plugins-config.json"), `"acme/greet": {}`, `"acme/plain": {}`)
	wantInOrder(t, readFile(t, dir, "go.mod"), "github.com/acme/collage-greet v0.1.0", "github.com/acme/collage-plain v0.2.0")
	goBuild(t, dir)
}

// Review Focus 3: a module that breaks the contract leaves every file as it was.
func TestPluginAdd_RefusedRestoresEverything(t *testing.T) {
	needGo(t)
	broken := fixturePlugin("acme", "broken", "v0.1.0", "func New(a, b string) *Plugin { return nil }", "")
	nomanifest := fixturePlugin("acme", "bare", "v0.1.0", "func New() *Plugin { return nil }", "")
	delete(nomanifest.Files, "collage.json")
	dir := pluginProject(t, broken, nomanifest)
	before := map[string]string{}
	for _, f := range []string{"go.mod", "go.sum", "plugins.go", "plugins-config.json"} {
		if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
			before[f] = string(b)
		}
	}
	for _, ref := range []string{"acme/broken", "acme/bare"} {
		code, _, errOut := pluginCmd(t, dir, "add", ref)
		if code != 1 || !strings.Contains(errOut, "collage: plugin add: ") {
			t.Fatalf("%s: code %d\n%s", ref, code, errOut)
		}
		for _, f := range []string{"go.mod", "go.sum", "plugins.go", "plugins-config.json"} {
			b, err := os.ReadFile(filepath.Join(dir, f))
			was, existed := before[f]
			if existed != (err == nil) || (existed && string(b) != was) {
				t.Errorf("%s: %s changed", ref, f)
			}
		}
	}
}

// The scaffold pluginProject makes builds before a plugin is added: its
// collage is this repository, offline.
func TestPluginProject_Builds(t *testing.T) {
	needGo(t)
	goBuild(t, pluginProject(t))
}

func TestPluginAdd_Output(t *testing.T) {
	needGo(t)
	dir := pluginProject(t, fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""))
	writeFiles(t, dir, map[string]string{"plugins-config.json": "{\n  \"acme/greet\": {\"greeting\": \"hi\"}\n}\n"})
	code, out, errOut := pluginCmd(t, dir, "add", "acme/greet@v0.1.0")
	if code != 0 {
		t.Fatalf("add = %d\n%s%s", code, out, errOut)
	}
	want := "added acme/greet v0.1.0\n" +
		"  plugins.go           greet.New()\n" +
		"  plugins-config.json  \"acme/greet\" kept as it was\n"
	if !strings.HasPrefix(out, want) {
		t.Errorf("output =\n%s\nwant it to start with\n%s", out, want)
	}
	if got := readFile(t, dir, "plugins-config.json"); got != "{\n  \"acme/greet\": {\"greeting\": \"hi\"}\n}\n" {
		t.Errorf("plugins-config.json changed:\n%s", got)
	}
}

func TestPluginAdd_PseudoVersionRefused(t *testing.T) {
	needGo(t)
	const pseudo = "v0.0.0-20261001000000-abcdefabcdef"
	dir := pluginProject(t, fixturePlugin("acme", "greet", pseudo, "func New() *Plugin { return &Plugin{} }", ""))
	gomod := readFile(t, dir, "go.mod")
	code, _, errOut := pluginCmd(t, dir, "add", "acme/greet@"+pseudo)
	if code != 1 || !strings.Contains(errOut, "collage: plugin add: github.com/acme/collage-greet "+pseudo+" is not a release; tag one (git tag v0.1.0) and push it") {
		t.Fatalf("code %d\n%s", code, errOut)
	}
	if readFile(t, dir, "go.mod") != gomod {
		t.Error("go.mod changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins.go")); err == nil {
		t.Error("plugins.go was written")
	}
}

func TestPluginAdd_GoGetFailureRestores(t *testing.T) {
	needGo(t)
	dir := pluginProject(t)
	gomod := readFile(t, dir, "go.mod")
	code, _, errOut := pluginCmd(t, dir, "add", "acme/missing")
	if code != 1 || !strings.Contains(errOut, "collage: plugin add: ") || !strings.Contains(errOut, "collage-missing") {
		t.Fatalf("code %d\n%s", code, errOut)
	}
	if readFile(t, dir, "go.mod") != gomod {
		t.Error("go.mod changed")
	}
}

func TestPluginAdd_NoPackageMain(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"go.mod": "module x\n", "lib.go": "package lib\n"})
	code, _, errOut := pluginCmd(t, dir, "add", "acme/greet")
	if code != 1 || !strings.Contains(errOut, "collage: plugin add: "+dir+" holds no package main; pass the project's directory with -dir") {
		t.Fatalf("code %d\n%s", code, errOut)
	}
}

func TestPluginAdd_NoGoMod(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	code, _, errOut := pluginCmd(t, dir, "add", "acme/greet")
	if code != 1 || !strings.Contains(errOut, "collage: plugin add: no go.mod in "+dir) {
		t.Fatalf("code %d\n%s", code, errOut)
	}
}

func TestPlugin_Usage(t *testing.T) {
	for _, args := range [][]string{nil, {"frobnicate"}, {"add"}, {"add", "a/b", "c/d"}, {"add", "not a ref"}} {
		c, _, errOut := testCLI()
		if code := c.Run(context.Background(), append([]string{"plugin"}, args...)); code != 2 {
			t.Errorf("plugin %v = %d, want 2\n%s", args, code, errOut)
		}
	}
	c, out, _ := testCLI()
	if code := c.Run(context.Background(), []string{"help", "plugin"}); code != 0 || !strings.Contains(out.String(), "collage plugin add <owner/name | module path>[@version] [-dir dir]") {
		t.Errorf("help plugin = %d\n%s", code, out)
	}
	c, out, _ = testCLI()
	if code := c.Run(context.Background(), []string{"plugin", "help"}); code != 0 || !strings.Contains(out.String(), plugincontract.AuthorsURL) {
		t.Errorf("plugin help = %d\n%s", code, out)
	}
}

// An existing entry's order is read from its manifest: a plugin of a lower
// order goes before it.
func TestPluginAdd_Order(t *testing.T) {
	needGo(t)
	dir := pluginProject(t,
		fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"order": 10`),
		fixturePlugin("acme", "early", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"order": 5`),
		fixturePlugin("acme", "late", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"order": 20`),
	)
	for _, ref := range []string{"acme/greet", "acme/late", "acme/early"} {
		if code, out, errOut := pluginCmd(t, dir, "add", ref); code != 0 {
			t.Fatalf("add %s = %d\n%s%s", ref, code, out, errOut)
		}
	}
	wantInOrder(t, readFile(t, dir, "plugins.go"), "early.New(),", "greet.New(),", "late.New(),")
	goBuild(t, dir)
}

// projectFiles are the files add may change, with their content; absent files
// are missing from the map.
func projectFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, f := range []string{"go.mod", "go.sum", "plugins.go", "plugins-config.json"} {
		if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
			files[f] = string(b)
		}
	}
	return files
}

func wantSameFiles(t *testing.T, dir string, before map[string]string) {
	t.Helper()
	after := projectFiles(t, dir)
	for _, f := range []string{"go.mod", "go.sum", "plugins.go", "plugins-config.json"} {
		was, existed := before[f]
		is, exists := after[f]
		if existed != exists || was != is {
			t.Errorf("%s changed:\nbefore (%v) %q\nafter  (%v) %q", f, existed, was, exists, is)
		}
	}
}

// interruptedTidy runs go for real, except "go mod tidy", which fails as a
// child killed by Ctrl-C does.
type interruptedTidy struct{}

func (interruptedTidy) Run(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error {
	if name == "go" && len(args) >= 2 && args[0] == "mod" && args[1] == "tidy" {
		return context.Canceled
	}
	return execRunner{}.Run(ctx, dir, env, stdout, stderr, name, args...)
}

// Review fix 1: a step interrupted after plugins.go and the config are
// written still puts all four files back.
func TestPluginAdd_InterruptedTidyRestores(t *testing.T) {
	needGo(t)
	dir := pluginProject(t, fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""))
	writeFiles(t, dir, map[string]string{
		"plugins.go":          pluginsSkeleton,
		"plugins-config.json": "{\n  \"other/thing\": {\"a\": 1}\n}\n",
		"go.sum":              "",
	})
	before := projectFiles(t, dir)
	c, out, errOut := testCLI()
	c.Runner = interruptedTidy{}
	code := c.Run(context.Background(), []string{"plugin", "add", "acme/greet", "-dir", dir})
	if code != 1 || !strings.Contains(errOut.String(), "collage: plugin add: ") {
		t.Fatalf("add = %d\n%s%s", code, out, errOut)
	}
	wantSameFiles(t, dir, before)
}

// panickingTidy runs go for real, except "go mod tidy", which panics: a bug
// in a step after the writes.
type panickingTidy struct{}

func (panickingTidy) Run(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error {
	if name == "go" && len(args) >= 2 && args[0] == "mod" && args[1] == "tidy" {
		panic("tidy exploded")
	}
	return execRunner{}.Run(ctx, dir, env, stdout, stderr, name, args...)
}

// Review fix 1: a panic after the writes restores the files, then goes on.
func TestPluginAdd_PanicRestores(t *testing.T) {
	needGo(t)
	dir := pluginProject(t, fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""))
	before := projectFiles(t, dir)
	c, _, _ := testCLI()
	c.Runner = panickingTidy{}
	func() {
		defer func() {
			if r := recover(); r != "tidy exploded" {
				t.Errorf("recovered %v, want the runner's panic", r)
			}
		}()
		c.Run(context.Background(), []string{"plugin", "add", "acme/greet", "-dir", dir})
	}()
	wantSameFiles(t, dir, before)
}

// Review fix 2: plugins.go is written before plugins-config.json is read; a
// config that is not JSON undoes the plugins.go write.
func TestPluginAdd_BadConfigUndoesPluginsGo(t *testing.T) {
	needGo(t)
	mod := fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", "")
	for name, pluginsGo := range map[string]string{
		"absent":   "",
		"existing": strings.Replace(pluginsSkeleton, "func plugins()", "// mine, keep me\nfunc plugins()", 1),
	} {
		t.Run(name, func(t *testing.T) {
			dir := pluginProject(t, mod)
			files := map[string]string{"plugins-config.json": "{"}
			if pluginsGo != "" {
				files["plugins.go"] = pluginsGo
			}
			writeFiles(t, dir, files)
			before := projectFiles(t, dir)
			code, out, errOut := pluginCmd(t, dir, "add", "acme/greet")
			if code != 1 || !strings.Contains(errOut, "collage: plugin add: ") {
				t.Fatalf("add = %d\n%s%s", code, out, errOut)
			}
			wantSameFiles(t, dir, before)
		})
	}
}

// Review fix 3: a package main file go build would not compile does not make
// the directory a program's.
func TestPluginAdd_IgnoredMainIsNoMain(t *testing.T) {
	fakeProxy(t) // should the check let it through, go get stays offline
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":  "module x\n",
		"gen.go":  "//go:build ignore\n\npackage main\n\nfunc main() {}\n",
		"lib.go":  "package lib\n",
		"_old.go": "package main\n",
	})
	code, _, errOut := pluginCmd(t, dir, "add", "acme/greet")
	if code != 1 || !strings.Contains(errOut, "holds no package main") {
		t.Fatalf("code %d\n%s", code, errOut)
	}
}
