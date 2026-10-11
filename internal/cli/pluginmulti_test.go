package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// withBundles replaces the bundle table for one test.
func withBundles(t *testing.T, b []bundle) {
	t.Helper()
	was := bundles
	bundles = b
	t.Cleanup(func() { bundles = was })
}

func multiFixtures() []fakeModule {
	return []fakeModule{
		fixturePlugin("acme", "greet", "v0.1.0", "func New(opts Options) *Plugin { return &Plugin{opts: opts} }", ""),
		fixturePlugin("acme", "plain", "v0.2.0", "func New() *Plugin { return &Plugin{} }", `"setup": "Pass it nothing."`),
		fixturePlugin("acme", "broken", "v0.1.0", "func New(a, b string) *Plugin { return nil }", ""),
		fixturePlugin("acme", "base", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""),
		fixturePlugin("acme", "ext", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"requires": ["acme/base"]`),
	}
}

// Each plugin is added on its own: one that fails is put back, and the ones
// around it stay.
func TestPluginAdd_Many(t *testing.T) {
	needGo(t)
	dir := pluginProject(t, multiFixtures()...)
	code, out, errOut := pluginCmd(t, dir, "add", "acme/greet", "acme/broken", "acme/plain")
	if code != 1 {
		t.Fatalf("add = %d, want 1\n%s%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "collage: plugin add: acme/broken: ") {
		t.Errorf("stderr does not name the failure:\n%s", errOut)
	}
	wantInOrder(t, out, "added acme/greet v0.1.0", "added acme/plain v0.2.0",
		"added: acme/greet, acme/plain", "failed: acme/broken")
	plugins := readFile(t, dir, "plugins.go")
	wantInOrder(t, plugins, "greet.New(greet.Options{}),", "plain.New(),")
	if strings.Contains(plugins, "broken") || strings.Contains(readFile(t, dir, "go.mod"), "broken") {
		t.Errorf("the failed plugin was left behind:\n%s", plugins)
	}
	goBuild(t, dir)
}

// Setup notes wait for the end, under one heading, when there are several.
func TestPluginAdd_ManySetupNotesLast(t *testing.T) {
	needGo(t)
	dir := pluginProject(t, multiFixtures()...)
	code, out, errOut := pluginCmd(t, dir, "add", "acme/plain", "acme/greet")
	if code != 0 {
		t.Fatalf("add = %d\n%s%s", code, out, errOut)
	}
	wantInOrder(t, out, "added acme/plain", "added acme/greet", "added: acme/plain, acme/greet",
		"To finish setting up:", "acme/plain", "Pass it nothing.")
	if strings.Count(out, "Pass it nothing.") != 1 {
		t.Errorf("the setup note is printed more than once:\n%s", out)
	}
}

// A plugin named twice is added once, and one alone prints no summary.
func TestPluginAdd_ManyDuplicate(t *testing.T) {
	needGo(t)
	dir := pluginProject(t, multiFixtures()...)
	code, out, errOut := pluginCmd(t, dir, "add", "acme/plain", "ACME/Plain")
	if code != 0 {
		t.Fatalf("add = %d\n%s%s", code, out, errOut)
	}
	if strings.Count(readFile(t, dir, "plugins.go"), "plain.New()") != 1 || strings.Contains(out, "added:") {
		t.Errorf("duplicate:\n%s\n%s", out, readFile(t, dir, "plugins.go"))
	}
	if code, _, e := pluginCmd(t, dir, "add", "acme/plain@v0.1.0", "acme/plain@v0.2.0"); code != 2 || !strings.Contains(e, "two versions") {
		t.Errorf("two versions = %d\n%s", code, e)
	}
}

// A reference that cannot be read stops the command before anything changes.
func TestPluginAdd_ManyBadRef(t *testing.T) {
	needGo(t)
	dir := pluginProject(t, multiFixtures()...)
	before := projectFiles(t, dir)
	if code, _, e := pluginCmd(t, dir, "add", "acme/greet", "a/b/c"); code != 2 {
		t.Fatalf("add = %d, want 2\n%s", code, e)
	}
	wantSameFiles(t, dir, before)
}

func TestPluginAdd_Bundle(t *testing.T) {
	needGo(t)
	withBundles(t, []bundle{{Name: "kit", Short: "a kit", Plugins: []string{"acme/greet", "acme/plain"}}})
	dir := pluginProject(t, multiFixtures()...)
	code, out, errOut := pluginCmd(t, dir, "add", "@kit")
	if code != 0 {
		t.Fatalf("add @kit = %d\n%s%s", code, out, errOut)
	}
	wantInOrder(t, readFile(t, dir, "plugins.go"), "greet.New(greet.Options{}),", "plain.New(),")
	wantInOrder(t, out, "added: acme/greet, acme/plain")

	for _, bad := range []string{"@nope", "@"} {
		code, _, e := pluginCmd(t, dir, "add", bad)
		if code != 2 || !strings.Contains(e, "@kit") {
			t.Errorf("add %s = %d; want 2 and the bundles listed\n%s", bad, code, e)
		}
	}
}

// del takes several too, and removes a plugin another one on the list
// requires after that one, whatever the order they were given in.
func TestPluginDel_Many(t *testing.T) {
	needGo(t)
	dir := pluginProject(t, multiFixtures()...)
	if code, _, e := pluginCmd(t, dir, "add", "acme/ext", "acme/greet"); code != 0 {
		t.Fatal(e)
	}
	code, out, errOut := pluginCmd(t, dir, "del", "acme/base", "acme/ext", "acme/greet")
	if code != 0 {
		t.Fatalf("del = %d\n%s%s", code, out, errOut)
	}
	wantInOrder(t, out, "removed acme/ext", "removed acme/greet", "removed acme/base",
		"removed: acme/ext, acme/greet, acme/base")
	if p := readFile(t, dir, "plugins.go"); strings.Contains(p, "acme") {
		t.Errorf("plugins.go still has plugins:\n%s", p)
	}
	goBuild(t, dir)

	if code, _, e := pluginCmd(t, dir, "add", "acme/greet"); code != 0 {
		t.Fatal(e)
	}
	code, out, errOut = pluginCmd(t, dir, "del", "acme/nope", "acme/greet")
	if code != 1 || !strings.Contains(errOut, "acme/nope is not in plugins.go") {
		t.Fatalf("del = %d\n%s%s", code, out, errOut)
	}
	wantInOrder(t, out, "removed acme/greet", "removed: acme/greet", "failed: acme/nope")
}

// A required plugin whose dependant stays is still refused.
func TestPluginDel_ManyStillRequired(t *testing.T) {
	needGo(t)
	dir := pluginProject(t, multiFixtures()...)
	if code, _, e := pluginCmd(t, dir, "add", "acme/ext", "acme/greet"); code != 0 {
		t.Fatal(e)
	}
	code, _, errOut := pluginCmd(t, dir, "del", "acme/base", "acme/greet")
	if code != 1 || strings.Count(errOut, "acme/ext requires acme/base") != 1 {
		t.Fatalf("del = %d\n%s", code, errOut)
	}
}

// del @bundle removes the bundle's plugins that are there and passes over
// the rest.
func TestPluginDel_Bundle(t *testing.T) {
	needGo(t)
	withBundles(t, []bundle{{Name: "kit", Short: "a kit", Plugins: []string{"acme/greet", "acme/plain"}}})
	dir := pluginProject(t, multiFixtures()...)
	if code, _, e := pluginCmd(t, dir, "add", "acme/greet"); code != 0 {
		t.Fatal(e)
	}
	code, out, errOut := pluginCmd(t, dir, "del", "@kit")
	if code != 0 {
		t.Fatalf("del @kit = %d\n%s%s", code, out, errOut)
	}
	wantInOrder(t, out, "removed acme/greet", "removed: acme/greet", "not added: acme/plain")
}

// The bundles collage ships name only short references, each once, and every
// name starts with a letter.
func TestBundles_Table(t *testing.T) {
	if len(bundles) == 0 {
		t.Fatal("no bundles")
	}
	for _, b := range bundles {
		if b.Name == "" || strings.HasPrefix(b.Name, "@") || b.Short == "" || len(b.Plugins) == 0 {
			t.Errorf("bundle %+v", b)
		}
		refs, err := expandRefs([]string{"@" + b.Name})
		if err != nil {
			t.Errorf("@%s: %v", b.Name, err)
			continue
		}
		if len(refs) != len(b.Plugins) {
			t.Errorf("@%s repeats a plugin", b.Name)
		}
		for _, r := range refs {
			if r.ref.Short == "" {
				t.Errorf("@%s: %s is not owner/name", b.Name, r.label)
			}
		}
	}
	if !strings.Contains(pluginUsage(), "@prod") {
		t.Error("the usage does not list the bundles")
	}
}

// newWithProject runs "collage new" with args in a fresh directory whose
// collage is this repository, offline.
func newWithProject(t *testing.T, mods []fakeModule, args ...string) (string, int, string, string) {
	t.Helper()
	fakeProxy(t, mods...)
	was := prepareWith
	prepareWith = func(_ context.Context, _ *CLI, dir string) error {
		for _, a := range [][]string{
			{"mod", "edit", "-replace", "github.com/Elagoht/collage=" + repoRootForTest(t)},
			{"mod", "edit", "-require", "github.com/Elagoht/collage@v0.57.0"},
		} {
			cmd := exec.Command("go", a...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("go %v: %v\n%s", a, err, out)
			}
		}
		return nil
	}
	t.Cleanup(func() { prepareWith = was })
	dir := filepath.Join(t.TempDir(), "app")
	c, out, errOut := testCLI()
	code := c.Run(context.Background(), append([]string{"new", "app", "-dir", dir, "-module", "withapp"}, args...))
	return dir, code, out.String(), errOut.String()
}

func TestNew_With(t *testing.T) {
	needGo(t)
	withBundles(t, []bundle{{Name: "kit", Short: "a kit", Plugins: []string{"acme/greet", "acme/plain"}}})
	dir, code, out, errOut := newWithProject(t, multiFixtures(), "--with", "@kit,acme/base")
	if code != 0 {
		t.Fatalf("new --with = %d\n%s%s", code, out, errOut)
	}
	wantInOrder(t, out, "Scaffolded", "added acme/greet", "added acme/plain", "added acme/base",
		"added: acme/greet, acme/plain, acme/base", "To finish setting up:", "Pass it nothing.", "Next steps:")
	if strings.Contains(out, "go mod tidy") {
		t.Errorf("next steps still ask for go mod tidy:\n%s", out)
	}
	wantInOrder(t, readFile(t, dir, "plugins.go"), "greet.New(greet.Options{}),", "plain.New(),", "base.New(),")
	goBuild(t, dir)
}

// --with repeats, and a plugin that fails leaves the project and the others.
func TestNew_WithFailure(t *testing.T) {
	needGo(t)
	dir, code, out, errOut := newWithProject(t, multiFixtures(), "--with", "acme/plain", "--with", "acme/broken")
	if code != 1 {
		t.Fatalf("new --with = %d, want 1\n%s%s", code, out, errOut)
	}
	wantInOrder(t, out, "Scaffolded", "added acme/plain", "failed: acme/broken", "Next steps:")
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
		t.Error("the project is gone")
	}
	goBuild(t, dir)
}

// A --with that cannot be read is refused before anything is written.
func TestNew_WithRefusedFirst(t *testing.T) {
	for _, with := range []string{"@nope", "prod", "acme/x,a/b/c", ""} {
		dir := filepath.Join(t.TempDir(), "app")
		c, _, errOut := testCLI()
		code := c.Run(context.Background(), []string{"new", "app", "-dir", dir, "--with", with})
		if code != 2 {
			t.Errorf("--with %q = %d, want 2\n%s", with, code, errOut)
		}
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("--with %q scaffolded anyway", with)
		}
	}
}
