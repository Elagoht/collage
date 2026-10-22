package cli

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/cli/plugincontract"
)

func TestPluginCheck_Static(t *testing.T) {
	good := fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", "")
	dir := t.TempDir()
	writeFiles(t, dir, good.Files)
	writeFiles(t, dir, map[string]string{"go.mod": "module github.com/acme/collage-greet\n\ngo 1.26\n"})
	c, out, errOut := testCLI()
	if code := c.Run(t.Context(), []string{"plugin", "check", dir}); code != 0 {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if !strings.Contains(out.String(), "acme/greet: meets the contract") {
		t.Fatal(out.String())
	}
	writeFiles(t, dir, map[string]string{"collage.json": `{"name": "Bad"}`})
	c, _, errOut = testCLI()
	if code := c.Run(t.Context(), []string{"plugin", "check", dir}); code != 1 || !strings.Contains(errOut.String(), "lowercase") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestPluginCheck_Release(t *testing.T) {
	needGo(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	good := fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", "")
	dir := t.TempDir()
	writeFiles(t, dir, good.Files)
	writeFiles(t, dir, map[string]string{"go.mod": "module github.com/acme/collage-greet\n\ngo 1.26\n"})
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-qm", "x")
	c, _, errOut := testCLI()
	if code := c.Run(t.Context(), []string{"plugin", "check", "-release", dir}); code != 1 || !strings.Contains(errOut.String(), "no release tag at HEAD") {
		t.Fatalf("untagged: %d %s", code, errOut)
	}
	// A pseudo-version tag is not a release, even when it sorts highest.
	git("tag", "v0.1.1-0.20261001000000-abcdefabcdef")
	c, _, errOut = testCLI()
	if code := c.Run(t.Context(), []string{"plugin", "check", "-release", dir}); code != 1 || !strings.Contains(errOut.String(), "no release tag at HEAD") {
		t.Fatalf("pseudo only: %d %s", code, errOut)
	}
	git("tag", "v0.1.0")
	c, out, errOut := testCLI()
	if code := c.Run(t.Context(), []string{"plugin", "check", "-release", dir}); code != 0 || !strings.Contains(out.String(), "v0.1.0 at HEAD is a release") {
		t.Fatalf("tagged: %s %s", out, errOut)
	}
}

func TestCompareSemver(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int // its sign
	}{
		{"v1.0.0", "v1.0.0", 0},
		{"v1.0.0", "v1.0.0-rc.1", 1},
		{"v1.0.0-rc.1", "v1.0.0", -1},
		{"v1.10.0", "v1.9.0", 1},
		{"v2.0.0", "v10.0.0", -1},
		{"v1.0.0-alpha.2", "v1.0.0-alpha.10", -1},   // numeric identifiers compare as numbers
		{"v1.0.0-alpha.1", "v1.0.0-alpha.beta", -1}, // numeric before alphanumeric
		{"v1.0.0-beta", "v1.0.0-alpha", 1},
		{"v1.0.0-alpha", "v1.0.0-alpha.1", -1},  // fewer identifiers first
		{"v1.0.0+build.2", "v1.0.0+build.1", 0}, // build metadata is ignored
	} {
		got := compareSemver(tc.a, tc.b)
		if sign(got) != tc.want {
			t.Errorf("compareSemver(%q, %q) = %d, want sign %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}

func TestHighestReleaseTag(t *testing.T) {
	for _, tc := range []struct {
		tags, want string
	}{
		{"v0.1.0 v0.2.0 v0.1.5", "v0.2.0"},
		{"vfoo v0.1.0 latest", "v0.1.0"},
		{"v1.0.0-rc.1 v0.9.0", "v1.0.0-rc.1"},
		{"v0.1.1-0.20261001000000-abcdefabcdef v0.1.0", "v0.1.0"},
		{"v0.1.1-0.20261001000000-abcdefabcdef vfoo", ""},
		{"", ""},
	} {
		if got := highestReleaseTag(strings.Fields(tc.tags)); got != tc.want {
			t.Errorf("highestReleaseTag(%q) = %q, want %q", tc.tags, got, tc.want)
		}
	}
}

// Final review I3: the program -run builds compiles the call add writes,
// where plugins.go has it, and starts no application.
func TestCheckProgram(t *testing.T) {
	src := checkProgram("acme/greet", "github.com/acme/collage-greet", plugincontract.Constructor{Package: "greet", Arg: "Options"})
	for _, want := range []string{"var p collage.Plugin = plugin.New(plugin.Options{})", `p.Name() != "acme/greet"`} {
		if !strings.Contains(src, want) {
			t.Errorf("program lacks %s:\n%s", want, src)
		}
	}
	for _, unwanted := range []string{"collage.New(", ".Start()"} {
		if strings.Contains(src, unwanted) {
			t.Errorf("program has %s:\n%s", unwanted, src)
		}
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "main.go", src, 0); err != nil {
		t.Errorf("program does not parse: %v\n%s", err, src)
	}
}

func TestPluginCheck_Run(t *testing.T) {
	needGo(t)
	// The offline env; acme/other is served for the plugin that requires it.
	fakeProxy(t, fixturePlugin("acme", "other", "v0.1.0", "func New(o *Options) *Plugin { return &Plugin{} }", ""))
	const passed = "acme/greet: New runs, and Name() matches collage.json"
	for _, tc := range []struct {
		name, decl, want, errWant string
		code                      int
		manifest, req             string
		vendor                    bool
		edit                      func(src string) string
	}{
		{name: "works", decl: "func New() *Plugin { return &Plugin{} }", want: passed},
		{name: "options", decl: "func New(o Options) *Plugin { return &Plugin{opts: o} }", want: passed},
		// A required field an empty section lacks is the plugin's startup
		// error, not a contract failure: -run neither configures nor starts.
		{name: "Init refuses an empty config", decl: "func New() *Plugin { return &Plugin{} }", want: passed,
			edit: func(src string) string {
				src = strings.Replace(src, "opts, err := collage.PluginConfig(host, p.opts)",
					"if p.opts.Greeting == \"\" { return errors.New(\"greeting is required\") }\n\topts, err := collage.PluginConfig(host, p.opts)", 1)
				return strings.Replace(src, `"context"`, "\"context\"\n\t\"errors\"", 1)
			}},
		{name: "New panics", decl: "func New() *Plugin { panic(\"New exploded\") }", code: 1, errWant: "New exploded"},
		{name: "Name mismatch", decl: "func New() *Plugin { return &Plugin{} }", code: 1, errWant: `Name() is "acme/other", collage.json says "acme/greet"`,
			edit: func(src string) string {
				return strings.Replace(src, `return "acme/greet" }`, `return "acme/other" }`, 1)
			}},
		{name: "New returns no plugin", decl: "func New() int { return 0 }", code: 1, errWant: "collage.Plugin"},
		{name: "requires, absent", decl: "func New() *Plugin { return &Plugin{} }", manifest: `"requires": ["acme/other"]`, want: passed},
		{name: "requires, present", decl: "func New() *Plugin { return &Plugin{} }", manifest: `"requires": ["acme/other"]`,
			req: "require github.com/acme/collage-other v0.1.0\n\n", want: passed},
		// A required plugin the plugin's code does not import is not vendored;
		// the program does not need it.
		{name: "vendored, requires present", decl: "func New() *Plugin { return &Plugin{} }", manifest: `"requires": ["acme/other"]`,
			req: "require github.com/acme/collage-other v0.1.0\n\n", want: passed, vendor: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := fixturePlugin("acme", "greet", "v0.1.0", tc.decl, tc.manifest)
			if tc.edit != nil {
				m.Files["plugin.go"] = tc.edit(m.Files["plugin.go"])
			}
			dir := t.TempDir()
			writeFiles(t, dir, m.Files)
			writeFiles(t, dir, map[string]string{"go.mod": "module github.com/acme/collage-greet\n\ngo 1.26\n\nrequire github.com/Elagoht/collage v0.57.0\n\n" + tc.req + "replace github.com/Elagoht/collage => " + repoRootForTest(t) + "\n"})
			if tc.vendor {
				// Without the fake proxy's -mod=mod, so the go tool reads the
				// vendor directory as it would in a real repository.
				t.Setenv("GOFLAGS", testGoFlags+" -modcacherw")
				// A repository's go.sum is complete; go mod vendor alone
				// leaves out the module nothing imports.
				for _, args := range [][]string{{"mod", "download", "github.com/acme/collage-other"}, {"mod", "vendor"}} {
					cmd := exec.Command("go", args...)
					cmd.Dir = dir
					if b, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("go %v: %v\n%s", args, err, b)
					}
				}
			}
			before, err := snapshot(filepath.Join(dir, "go.mod"), filepath.Join(dir, "go.sum"), filepath.Join(dir, "go.work.sum"))
			if err != nil {
				t.Fatal(err)
			}
			c, out, errOut := testCLI()
			code := c.Run(t.Context(), []string{"plugin", "check", "-run", dir})
			// A check leaves the repository as it found it: go mod download
			// and -mod=mod would otherwise write go.sum.
			for path, b := range before {
				got, err := os.ReadFile(path)
				if b == nil && err == nil {
					t.Errorf("%s was created:\n%s", filepath.Base(path), got)
				} else if b != nil && string(got) != string(b) {
					t.Errorf("%s changed:\n%s", filepath.Base(path), got)
				}
			}
			if code != tc.code || !strings.Contains(out.String()+errOut.String(), tc.want) {
				t.Fatalf("%d\n%s\n%s", code, out, errOut)
			}
			if !strings.Contains(errOut.String(), tc.errWant) {
				t.Fatalf("the failure is not shown:\n%s", errOut)
			}
			// The throwaway program is gone, whether it started or not.
			if left, _ := filepath.Glob(filepath.Join(dir, "collage-plugin-check-*")); len(left) > 0 {
				t.Fatalf("left behind: %v", left)
			}
			if _, err := os.Stat(filepath.Join(dir, "plugin.go")); err != nil {
				t.Fatal(err)
			}
		})
	}
}
