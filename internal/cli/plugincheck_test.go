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

func TestModFlag(t *testing.T) {
	dir := t.TempDir()
	if got := modFlag(dir, true); len(got) != 0 {
		t.Errorf("not vendored: %v", got)
	}
	writeFiles(t, dir, map[string]string{"vendor/modules.txt": "# x\n"})
	if got := modFlag(dir, false); len(got) != 0 {
		t.Errorf("vendored, no requires: %v; want it built from vendor", got)
	}
	if got := modFlag(dir, true); len(got) != 1 || got[0] != "-mod=readonly" {
		t.Errorf("vendored, requires: %v", got)
	}
}

func TestCheckProgram_RequiredPluginsGetAConfigSection(t *testing.T) {
	src := checkProgram("acme/greet", "github.com/acme/collage-greet", plugincontract.Constructor{Package: "greet"},
		[]requiredPlugin{{Name: "acme/other", Path: "github.com/acme/collage-other", Constructor: plugincontract.Constructor{Package: "other", Arg: "nil"}}})
	for _, want := range []string{`"acme/other": json.RawMessage("{}")`, `"acme/greet": json.RawMessage("{}")`, "req0.New(nil), p}"} {
		if !strings.Contains(src, want) {
			t.Errorf("program lacks %s:\n%s", want, src)
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
	for _, tc := range []struct {
		name, decl, want string
		code             int
		manifest, req    string
		vendor           bool
	}{
		{name: "works", decl: "func New() *Plugin { return &Plugin{} }", want: "acme/greet: builds and starts"},
		{name: "options", decl: "func New(o Options) *Plugin { return &Plugin{opts: o} }", want: "acme/greet: builds and starts"},
		{name: "init fails", decl: "func New() *Plugin { return &Plugin{} }\nfunc init() { failInit = true }\nvar failInit bool", want: "init", code: 1},
		{name: "requires, absent", decl: "func New() *Plugin { return &Plugin{} }", manifest: `"requires": ["acme/other"]`,
			want: "acme/greet: acme/other is not among its requirements; checked without it\nacme/greet: builds and starts"},
		{name: "requires, present", decl: "func New() *Plugin { return &Plugin{} }", manifest: `"requires": ["acme/other"]`,
			req: "require github.com/acme/collage-other v0.1.0\n\n", want: "acme/greet: builds and starts"},
		// go list cannot compute "all" from a vendor directory, and acme/other
		// is not vendored: the plugin's code does not import it.
		{name: "vendored, requires present", decl: "func New() *Plugin { return &Plugin{} }", manifest: `"requires": ["acme/other"]`,
			req: "require github.com/acme/collage-other v0.1.0\n\n", want: "acme/greet: builds and starts", vendor: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := fixturePlugin("acme", "greet", "v0.1.0", tc.decl, tc.manifest)
			if tc.name == "init fails" {
				m.Files["plugin.go"] = strings.Replace(m.Files["plugin.go"], "opts, err := collage.PluginConfig(host, p.opts)",
					"if failInit { return errors.New(\"refuses an empty config\") }\n\topts, err := collage.PluginConfig(host, p.opts)", 1)
				m.Files["plugin.go"] = strings.Replace(m.Files["plugin.go"], `"context"`, "\"context\"\n\t\"errors\"", 1)
			}
			dir := t.TempDir()
			writeFiles(t, dir, m.Files)
			writeFiles(t, dir, map[string]string{"go.mod": "module github.com/acme/collage-greet\n\ngo 1.26\n\nrequire github.com/Elagoht/collage v0.57.0\n\n" + tc.req + "replace github.com/Elagoht/collage => " + repoRootForTest(t) + "\n"})
			if tc.vendor {
				// Without the fake proxy's -mod=mod, so the go tool reads the
				// vendor directory as it would in a real repository.
				t.Setenv("GOFLAGS", "-modcacherw")
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
			if tc.code != 0 && !strings.Contains(errOut.String(), "refuses an empty config") {
				t.Fatalf("the plugin's own error is not shown:\n%s", errOut)
			}
			if tc.req != "" && strings.Contains(out.String(), "not among") {
				t.Fatalf("the required plugin was left out:\n%s", out)
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
