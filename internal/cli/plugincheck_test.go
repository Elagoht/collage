package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	git("tag", "v0.1.0")
	c, _, errOut = testCLI()
	if code := c.Run(t.Context(), []string{"plugin", "check", "-release", dir}); code != 0 {
		t.Fatalf("tagged: %s", errOut)
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
			c, out, errOut := testCLI()
			code := c.Run(t.Context(), []string{"plugin", "check", "-run", dir})
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
