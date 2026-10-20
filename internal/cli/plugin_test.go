package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	// The scaffold's main.go already passes plugins() to collage.New.
	if strings.Contains(out, "plugins.go is new") {
		t.Errorf("add told a scaffolded project to wire plugins():\n%s", out)
	}
	if code, out, errOut := pluginCmd(t, dir, "add", "acme/plain"); code != 0 || !strings.Contains(out, "Pass it nothing.") {
		t.Fatalf("add plain = %d\n%s%s", code, out, errOut)
	}
	wantInOrder(t, readFile(t, dir, "plugins.go"), `greet "github.com/acme/collage-greet"`, `plain "github.com/acme/collage-plain"`,
		"greet.New(greet.Options{}),", "plain.New(),")
	wantInOrder(t, readFile(t, dir, "plugins-config.json"), `"acme/greet": {}`, `"acme/plain": {}`)
	wantInOrder(t, readFile(t, dir, "go.mod"), "github.com/acme/collage-greet v0.1.0", "github.com/acme/collage-plain v0.2.0")
	goBuild(t, dir)
}

// A main.go that does not call plugins() is told how to wire it.
func TestPluginAdd_HintWhenUnwired(t *testing.T) {
	needGo(t)
	dir := pluginProject(t, fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""))
	main := readFile(t, dir, "main.go")
	unwired := regexp.MustCompile(`Plugins:\s+plugins\(\),`).ReplaceAllString(main, "")
	if unwired == main {
		t.Fatal("main.go has no Plugins: plugins(), to remove")
	}
	writeFiles(t, dir, map[string]string{"main.go": unwired})
	code, out, errOut := pluginCmd(t, dir, "add", "acme/greet")
	if code != 0 {
		t.Fatalf("add = %d\n%s%s", code, out, errOut)
	}
	wantInOrder(t, out, "added acme/greet v0.1.0", "plugins.go is new: give it to collage.New once", "Plugins: plugins(),")
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
	gomod, plugins := readFile(t, dir, "go.mod"), readFile(t, dir, "plugins.go")
	code, _, errOut := pluginCmd(t, dir, "add", "acme/greet@"+pseudo)
	if code != 1 || !strings.Contains(errOut, "collage: plugin add: github.com/acme/collage-greet "+pseudo+" is not a release; tag one (git tag v0.1.0) and push it") {
		t.Fatalf("code %d\n%s", code, errOut)
	}
	if got := readFile(t, dir, "go.mod"); got != gomod || strings.Contains(got, "collage-greet") {
		t.Error("go.mod changed")
	}
	if readFile(t, dir, "plugins.go") != plugins {
		t.Error("plugins.go changed")
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
	for _, args := range [][]string{nil, {"frobnicate"}, {"add"}, {"del"}, {"add", "a/b", "@nope"}, {"add", "not a ref"}, {"del", "a/b", "x"}} {
		c, _, errOut := testCLI()
		if code := c.Run(context.Background(), append([]string{"plugin"}, args...)); code != 2 {
			t.Errorf("plugin %v = %d, want 2\n%s", args, code, errOut)
		}
	}
	c, out, _ := testCLI()
	if code := c.Run(context.Background(), []string{"help", "plugin"}); code != 0 || !strings.Contains(out.String(), "collage plugin add <plugin>... [-dir dir]") {
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
			} else if err := os.Remove(filepath.Join(dir, "plugins.go")); err != nil {
				t.Fatal(err) // the scaffold ships one; this case has none
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

func TestPluginAdd_Version(t *testing.T) {
	needGo(t)
	dir := pluginProject(t,
		fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""),
		fixturePlugin("acme", "greet", "v0.2.0", "func New() *Plugin { return &Plugin{} }", ""),
	)
	if code, _, e := pluginCmd(t, dir, "add", "acme/greet@v0.1.0"); code != 0 {
		t.Fatal(e)
	}
	code, out, e := pluginCmd(t, dir, "add", "acme/greet")
	if code != 0 || !strings.Contains(out, "acme/greet is already added; now at v0.2.0") {
		t.Fatalf("%d %s %s", code, out, e)
	}
	if strings.Count(readFile(t, dir, "plugins.go"), "greet.New()") != 1 {
		t.Fatal("a second entry")
	}
	if !strings.Contains(readFile(t, dir, "go.mod"), "github.com/acme/collage-greet v0.2.0") {
		t.Fatal("go.mod not upgraded")
	}
	if code, out, _ := pluginCmd(t, dir, "add", "acme/greet"); code != 0 || !strings.Contains(out, "acme/greet is already added") || strings.Contains(out, "now at") {
		t.Fatalf("no-op add: %d %s", code, out)
	}
	goBuild(t, dir)
}

func TestPluginAdd_Requires(t *testing.T) {
	needGo(t)
	dir := pluginProject(t,
		fixturePlugin("acme", "base", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""),
		fixturePlugin("acme", "ext", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"requires": ["acme/base"], "order": -1`),
		fixturePlugin("acme", "loop-a", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"requires": ["acme/loop-b"]`),
		fixturePlugin("acme", "loop-b", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"requires": ["acme/loop-a"]`),
	)
	code, out, e := pluginCmd(t, dir, "add", "acme/ext")
	if code != 0 {
		t.Fatal(e)
	}
	wantInOrder(t, out, "added acme/base", "added acme/ext")
	wantInOrder(t, readFile(t, dir, "plugins.go"), "ext.New(),", "base.New(),") // ext's order -1 puts it first
	goBuild(t, dir)
	before := projectFiles(t, dir)
	code, _, e = pluginCmd(t, dir, "add", "acme/loop-a")
	if code != 1 || !strings.Contains(e, "collage: plugin add: cycle: acme/loop-a requires acme/loop-b requires acme/loop-a") {
		t.Fatalf("cycle: %d %s", code, e)
	}
	wantSameFiles(t, dir, before)
}

// A required plugin already in plugins.go is not added again.
func TestPluginAdd_RequiresAlreadyPresent(t *testing.T) {
	needGo(t)
	dir := pluginProject(t,
		fixturePlugin("acme", "base", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""),
		fixturePlugin("acme", "ext", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"requires": ["acme/base"]`),
	)
	if code, _, e := pluginCmd(t, dir, "add", "acme/base"); code != 0 {
		t.Fatal(e)
	}
	code, out, e := pluginCmd(t, dir, "add", "acme/ext")
	if code != 0 || strings.Contains(out, "base") {
		t.Fatalf("%d %s %s", code, out, e)
	}
	if strings.Count(readFile(t, dir, "plugins.go"), "base.New()") != 1 {
		t.Fatal("base added twice")
	}
}

// Review Focus 4: typed in mixed case, served lowercase, declared mixed.
func TestPluginAdd_MixedCase(t *testing.T) {
	needGo(t)
	m := fixturePlugin("Acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", "")
	m.Files["collage.json"] = `{"name": "acme/greet"}` + "\n"
	m.Files["plugin.go"] = strings.Replace(m.Files["plugin.go"], `"Acme/greet"`, `"acme/greet"`, 1)
	lower := m
	lower.Served = "github.com/acme/collage-greet"
	dir := pluginProject(t, m, lower)
	code, out, e := pluginCmd(t, dir, "add", "Acme/Greet")
	if code != 0 || !strings.Contains(readFile(t, dir, "go.mod"), "github.com/Acme/collage-greet v0.1.0") {
		t.Fatalf("%d %s %s", code, out, e)
	}
	if !strings.Contains(readFile(t, dir, "plugins.go"), `"github.com/Acme/collage-greet"`) {
		t.Fatal("plugins.go does not import the declared path")
	}
	goBuild(t, dir)

	// del finds it by the mixed-case short ref and by the lowercase full path.
	for i, ref := range []string{"Acme/Greet", "github.com/acme/collage-greet"} {
		if i > 0 {
			if code, _, e := pluginCmd(t, dir, "add", "Acme/Greet"); code != 0 {
				t.Fatal(e)
			}
		}
		if code, out, e := pluginCmd(t, dir, "del", ref); code != 0 {
			t.Fatalf("del %s: %d %s %s", ref, code, out, e)
		}
		for _, f := range []string{"plugins.go", "plugins-config.json", "go.mod"} {
			if strings.Contains(strings.ToLower(readFile(t, dir, f)), "greet") {
				t.Errorf("del %s: %s still names greet:\n%s", ref, f, readFile(t, dir, f))
			}
		}
		goBuild(t, dir)
	}
}

// failureRecorder runs go for real and records each go subcommand that failed.
type failureRecorder struct{ failed *[]string }

func (r failureRecorder) Run(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error {
	err := execRunner{}.Run(ctx, dir, env, stdout, stderr, name, args...)
	if err != nil && len(args) > 0 {
		*r.failed = append(*r.failed, strings.Join(args[:min(2, len(args))], " "))
	}
	return err
}

// Review Focus 3: go mod tidy fails after every file was written. The fixture
// has a file behind a build tag importing a package that does not exist: go
// get loads packages with the default tags and succeeds, tidy considers every
// tag and fails.
func TestPluginAdd_TidyFailureRestores(t *testing.T) {
	needGo(t)
	bad := fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", "")
	bad.Files["extra.go"] = "//go:build collageextra\n\npackage greet\n\nimport _ \"github.com/Elagoht/collage/pkg/nosuchpackage\"\n"
	dir := pluginProject(t, bad)
	before := projectFiles(t, dir)
	var failed []string
	c, out, errOut := testCLI()
	c.Runner = failureRecorder{failed: &failed}
	if code := c.Run(context.Background(), []string{"plugin", "add", "acme/greet", "-dir", dir}); code != 1 {
		t.Fatalf("add succeeded with a broken dependency: %s%s", out, errOut)
	}
	if len(failed) != 1 || failed[0] != "mod tidy" {
		t.Fatalf("failed go commands = %q, want only mod tidy\n%s", failed, errOut)
	}
	wantSameFiles(t, dir, before)
}

func TestRequiredVersion(t *testing.T) {
	gomod := []byte("module x\n\ngo 1.26\n\nrequire github.com/a/one v1.0.0\n\nrequire (\n\tgithub.com/a/two v0.2.0 // indirect\n\t\"github.com/a/three\" v0.3.0\n)\n\nreplace github.com/a/four => github.com/a/four v0.4.0\n")
	for path, want := range map[string]string{
		"github.com/a/one": "v1.0.0", "github.com/A/Two": "v0.2.0", "github.com/a/three": "v0.3.0",
		"github.com/a/four": "", "x": "",
	} {
		if got := requiredVersion(gomod, path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestCycleError(t *testing.T) {
	self := &cycleError{chain: []string{"a/a", "a/a"}, closing: "a/a"}
	if !self.closed() || self.Error() != "cycle: a/a requires a/a" {
		t.Errorf("%v %s", self.closed(), self)
	}
	if (&cycleError{chain: []string{"a/b", "a/a"}, closing: "a/a"}).closed() {
		t.Error("an open chain reported closed")
	}
}

// Fix round 1: a bare add never moves a plugin down, here from a prerelease
// above the latest release.
func TestPluginAdd_NoDowngrade(t *testing.T) {
	needGo(t)
	dir := pluginProject(t,
		fixturePlugin("acme", "greet", "v0.2.0", "func New() *Plugin { return &Plugin{} }", ""),
		fixturePlugin("acme", "greet", "v0.3.0-rc.1", "func New() *Plugin { return &Plugin{} }", ""),
	)
	if code, _, e := pluginCmd(t, dir, "add", "acme/greet@v0.3.0-rc.1"); code != 0 {
		t.Fatal(e)
	}
	code, out, e := pluginCmd(t, dir, "add", "acme/greet")
	if code != 0 || !strings.Contains(out, "acme/greet is already added") || strings.Contains(out, "now at") {
		t.Fatalf("%d %s %s", code, out, e)
	}
	if !strings.Contains(readFile(t, dir, "go.mod"), "github.com/acme/collage-greet v0.3.0-rc.1") {
		t.Fatalf("go.mod moved:\n%s", readFile(t, dir, "go.mod"))
	}
}

// Fix round 1: A requires B and C, which both require D; one add gives one
// entry each.
func TestPluginAdd_RequiresDiamond(t *testing.T) {
	needGo(t)
	dir := pluginProject(t,
		fixturePlugin("acme", "a", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"requires": ["acme/b", "acme/c"]`),
		fixturePlugin("acme", "b", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"requires": ["acme/d"]`),
		fixturePlugin("acme", "c", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"requires": ["acme/d"]`),
		fixturePlugin("acme", "d", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""),
	)
	code, out, e := pluginCmd(t, dir, "add", "acme/a")
	if code != 0 {
		t.Fatalf("%d %s %s", code, out, e)
	}
	wantInOrder(t, out, "added acme/d", "added acme/b", "added acme/c", "added acme/a")
	src := readFile(t, dir, "plugins.go")
	for _, call := range []string{"a.New()", "b.New()", "c.New()", "d.New()"} {
		if n := strings.Count(src, "\t"+call); n != 1 {
			t.Errorf("%s appears %d times:\n%s", call, n, src)
		}
	}
	goBuild(t, dir)
}

func TestPluginDel(t *testing.T) {
	needGo(t)
	dir := pluginProject(t,
		fixturePlugin("acme", "base", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""),
		fixturePlugin("acme", "ext", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"requires": ["acme/base"]`),
		fixturePlugin("acme", "greet", "v0.1.0", "func New(o Options) *Plugin { return &Plugin{opts: o} }", ""),
	)
	for _, ref := range []string{"acme/ext", "acme/greet"} {
		if code, _, e := pluginCmd(t, dir, "add", ref); code != 0 {
			t.Fatal(e)
		}
	}
	// Settings typed into the section are printed when it goes.
	cfg := strings.Replace(readFile(t, dir, "plugins-config.json"), `"acme/greet": {}`, `"acme/greet": {"greeting": "selam"}`, 1)
	writeFiles(t, dir, map[string]string{"plugins-config.json": cfg})

	if code, _, e := pluginCmd(t, dir, "del", "acme/base"); code != 1 || !strings.Contains(e, "acme/ext requires acme/base; remove it first") {
		t.Fatalf("dependent: %d %s", code, e)
	}
	code, out, e := pluginCmd(t, dir, "del", "ACME/Greet")
	if code != 0 || !strings.Contains(out, `{"greeting": "selam"}`) {
		t.Fatalf("%d %s %s", code, out, e)
	}
	for _, f := range []string{"plugins.go", "plugins-config.json", "go.mod"} {
		if strings.Contains(strings.ToLower(readFile(t, dir, f)), "greet") {
			t.Errorf("%s still names greet", f)
		}
	}
	if code, _, e := pluginCmd(t, dir, "del", "github.com/acme/collage-ext"); code != 0 {
		t.Fatal(e)
	}
	if code, _, e := pluginCmd(t, dir, "del", "acme/nope"); code != 1 || !strings.Contains(e, "acme/base") {
		t.Fatalf("unknown: %d %s (the error lists what plugins.go has)", code, e)
	}
	goBuild(t, dir)
}

// failingTidy runs go for real, except "go mod tidy", which fails with a
// message.
type failingTidy struct{}

func (failingTidy) Run(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error {
	if name == "go" && len(args) >= 2 && args[0] == "mod" && args[1] == "tidy" {
		io.WriteString(stderr, "tidy broke")
		return errors.New("exit status 1")
	}
	return execRunner{}.Run(ctx, dir, env, stdout, stderr, name, args...)
}

func TestPluginDel_TidyFailureRestores(t *testing.T) {
	needGo(t)
	dir := pluginProject(t, fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""))
	if code, _, e := pluginCmd(t, dir, "add", "acme/greet"); code != 0 {
		t.Fatal(e)
	}
	before := projectFiles(t, dir)
	c, out, errOut := testCLI()
	c.Runner = failingTidy{}
	code := c.Run(context.Background(), []string{"plugin", "del", "acme/greet", "-dir", dir})
	if code != 1 || !strings.Contains(errOut.String(), "collage: plugin del: tidy broke") {
		t.Fatalf("del = %d\n%s%s", code, out, errOut)
	}
	wantSameFiles(t, dir, before)
}

// Final review I1: an import name may not clash with a name another file of
// package main declares; the project still builds.
func TestPluginAdd_ClashWithOtherFile(t *testing.T) {
	needGo(t)
	dir := pluginProject(t, fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""))
	writeFiles(t, dir, map[string]string{"helpers.go": "package main\n\nfunc greet() string { return \"hi\" }\n\nvar _ = greet\n"})
	code, out, errOut := pluginCmd(t, dir, "add", "acme/greet")
	if code != 0 {
		t.Fatalf("add = %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(readFile(t, dir, "plugins.go"), `acmeGreet "github.com/acme/collage-greet"`) {
		t.Fatalf("plugins.go:\n%s", readFile(t, dir, "plugins.go"))
	}
	goBuild(t, dir)
}

// Final review I4: with an empty module cache, as after a fresh clone, del
// still reads the manifests: the dependents guard holds, and a del by full
// path removes the config section.
func TestPluginDel_ColdModuleCache(t *testing.T) {
	needGo(t)
	dir := pluginProject(t,
		fixturePlugin("acme", "base", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""),
		fixturePlugin("acme", "ext", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"requires": ["acme/base"]`),
	)
	if code, _, e := pluginCmd(t, dir, "add", "acme/ext"); code != 0 {
		t.Fatal(e)
	}
	t.Setenv("GOMODCACHE", t.TempDir())
	before := projectFiles(t, dir)
	if code, _, e := pluginCmd(t, dir, "del", "acme/base"); code != 1 || !strings.Contains(e, "acme/ext requires acme/base; remove it first") {
		t.Fatalf("dependent: %d %s", code, e)
	}
	wantSameFiles(t, dir, before)

	t.Setenv("GOMODCACHE", t.TempDir())
	if code, _, e := pluginCmd(t, dir, "del", "github.com/acme/collage-ext"); code != 0 {
		t.Fatal(e)
	}
	if cfg := readFile(t, dir, "plugins-config.json"); strings.Contains(cfg, "acme/ext") {
		t.Fatalf("the section is left:\n%s", cfg)
	}
	goBuild(t, dir)
}

// Final review I4: a manifest del cannot read refuses it, naming the module,
// rather than skipping the dependents guard.
func TestPluginDel_UnreadableManifestRefuses(t *testing.T) {
	needGo(t)
	dir := pluginProject(t,
		fixturePlugin("acme", "base", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""),
		fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""),
	)
	for _, ref := range []string{"acme/base", "acme/greet"} {
		if code, _, e := pluginCmd(t, dir, "add", ref); code != 0 {
			t.Fatal(e)
		}
	}
	// The cache is empty and nothing can fill it.
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOPROXY", "off")
	before := projectFiles(t, dir)
	for _, ref := range []string{"acme/greet", "github.com/acme/collage-greet"} {
		code, _, e := pluginCmd(t, dir, "del", ref)
		if code != 1 || !strings.Contains(e, "collage: plugin del: cannot read the collage.json of github.com/acme/collage-") {
			t.Fatalf("%s: %d %s", ref, code, e)
		}
		wantSameFiles(t, dir, before)
	}
}

// Final review I4: an entry whose module has no collage.json, wired by hand,
// requires nothing, so it does not stop a del.
func TestPluginDel_HandAddedEntry(t *testing.T) {
	needGo(t)
	bare := fixturePlugin("acme", "bare", "v0.1.0", "func New() *Plugin { return &Plugin{} }", "")
	delete(bare.Files, "collage.json")
	dir := pluginProject(t, bare, fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""))
	if code, _, e := pluginCmd(t, dir, "add", "acme/greet"); code != 0 {
		t.Fatal(e)
	}
	cmd := exec.Command("go", "get", "github.com/acme/collage-bare@v0.1.0")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go get: %v\n%s", err, out)
	}
	f, err := parsePluginsFile([]byte(readFile(t, dir, "plugins.go")))
	if err != nil {
		t.Fatal(err)
	}
	f.insert("github.com/acme/collage-bare", "bare", func(id string) string { return id + ".New()" }, 1, nil)
	src, err := f.format()
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{"plugins.go": string(src)})
	t.Setenv("GOMODCACHE", t.TempDir())
	if code, out, e := pluginCmd(t, dir, "del", "acme/greet"); code != 0 {
		t.Fatalf("%d %s %s", code, out, e)
	}
	goBuild(t, dir)
}

// Final review M2: a setup note reaches the terminal without its control
// characters, apart from newlines and tabs.
func TestPrintAdded_SetupNoteSanitised(t *testing.T) {
	c, out, _ := testCLI()
	c.printAdded([]addedPlugin{{Name: "acme/greet", Version: "v0.1.0", Call: "greet.New()", ConfigAdded: true,
		Setup: "\x1b[31mred\x1b[0m\rover\x07\u009b2J\nline two\n\tindented"}})
	got := out.String()
	if strings.ContainsAny(got, "\x1b\r\x07\u009b") {
		t.Fatalf("control characters printed: %q", got)
	}
	if !strings.Contains(got, "[31mred[0mover2J\nline two\n\tindented") {
		t.Fatalf("note = %q", got)
	}
}

// A plugin the application declares itself, in a package of its own module,
// has no manifest to read and stops no del.
func TestPluginDel_AppLocalPlugin(t *testing.T) {
	needGo(t)
	dir := pluginProject(t, fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""))
	if code, _, e := pluginCmd(t, dir, "add", "acme/greet"); code != 0 {
		t.Fatal(e)
	}
	writeFiles(t, dir, map[string]string{"myplugin/plugin.go": `package myplugin

import (
	"context"

	"github.com/Elagoht/collage/pkg/collage"
)

type Plugin struct{}

func New() *Plugin { return &Plugin{} }

func (*Plugin) Name() string                   { return "me/mine" }
func (*Plugin) Version() string                { return "0" }
func (*Plugin) Init(context.Context, collage.Host) error { return nil }
func (*Plugin) Shutdown(context.Context) error { return nil }
`})
	f, err := parsePluginsFile([]byte(readFile(t, dir, "plugins.go")))
	if err != nil {
		t.Fatal(err)
	}
	f.insert("pluginapp/myplugin", "myplugin", func(id string) string { return id + ".New()" }, 1, nil)
	src, err := f.format()
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{"plugins.go": string(src)})
	goBuild(t, dir)
	t.Setenv("GOMODCACHE", t.TempDir())
	if code, out, e := pluginCmd(t, dir, "del", "acme/greet"); code != 0 {
		t.Fatalf("%d %s %s", code, out, e)
	}
	goBuild(t, dir)
}

// handWire go-gets importPath and adds ident.New() for it to plugins.go, as
// a user editing the file would.
func handWire(t *testing.T, dir, importPath, version, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "get", importPath+"@"+version)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go get: %v\n%s", err, out)
	}
	f, err := parsePluginsFile([]byte(readFile(t, dir, "plugins.go")))
	if err != nil {
		t.Fatal(err)
	}
	f.insert(importPath, pkg, func(id string) string { return id + ".New()" }, 1, nil)
	src, err := f.format()
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{"plugins.go": string(src)})
	goBuild(t, dir)
}

// Re-review 1: an entry built from a subpackage of a dependency is not a
// contract plugin (rule 3); go list -m cannot locate it, and it stops no del.
func TestPluginDel_DependencySubpackageEntry(t *testing.T) {
	needGo(t)
	util := fixturePlugin("acme", "util", "v0.1.0", "func New() *Plugin { return &Plugin{} }", "")
	util.Files["sub/sub.go"] = "package sub\n\nimport util \"github.com/acme/collage-util\"\n\nfunc New() *util.Plugin { return util.New() }\n"
	dir := pluginProject(t, util, fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""))
	if code, _, e := pluginCmd(t, dir, "add", "acme/greet"); code != 0 {
		t.Fatal(e)
	}
	handWire(t, dir, "github.com/acme/collage-util/sub", "v0.1.0", "sub")
	if code, out, e := pluginCmd(t, dir, "del", "acme/greet"); code != 0 {
		t.Fatalf("%d %s %s", code, out, e)
	}
	goBuild(t, dir)
}

// Re-review 2: another entry's manifest is read leniently: a key from a
// newer manifest schema stops no del.
func TestPluginDel_NewerManifestKey(t *testing.T) {
	needGo(t)
	dir := pluginProject(t,
		fixturePlugin("acme", "newer", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"futureKey": 1`),
		fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""),
	)
	if code, _, e := pluginCmd(t, dir, "add", "acme/greet"); code != 0 {
		t.Fatal(e)
	}
	handWire(t, dir, "github.com/acme/collage-newer", "v0.1.0", "newer")
	if code, out, e := pluginCmd(t, dir, "del", "acme/greet"); code != 0 {
		t.Fatalf("%d %s %s", code, out, e)
	}
	goBuild(t, dir)
}
