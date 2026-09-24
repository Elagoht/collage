package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_New_MissingName(t *testing.T) {
	c, _, errOut := testCLI()

	code := c.Run(context.Background(), []string{"new"})

	if code != 2 {
		t.Fatalf("Run() = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "missing project name") {
		t.Errorf("stderr = %q, want it to mention the missing name", errOut.String())
	}
}

func TestRun_New_FlagsAfterName(t *testing.T) {
	// newUsage documents flags coming after <name>; the stdlib flag package
	// would otherwise stop parsing at the first positional argument and
	// misread every flag that follows it as more positional arguments. See
	// splitPositional.
	c, out, errOut := testCLI()
	dir := t.TempDir()
	target := filepath.Join(dir, "proj")

	code := c.Run(context.Background(), []string{"new", "demo", "-dir", target, "-module", "example.com/demo"})

	if code != 0 {
		t.Fatalf("Run() = %d, want 0; stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "demo") {
		t.Errorf("stdout = %q, want it to mention the project name", out.String())
	}
	modBytes, err := os.ReadFile(filepath.Join(target, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if !strings.Contains(string(modBytes), "module example.com/demo") {
		t.Errorf("go.mod = %q, want it to declare module example.com/demo", modBytes)
	}
}

func TestRun_New_DefaultDirAndModule(t *testing.T) {
	c, _, errOut := testCLI()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore Chdir: %v", err)
		}
	})

	code := c.Run(context.Background(), []string{"new", "myapp"})

	if code != 0 {
		t.Fatalf("Run() = %d, want 0; stderr = %s", code, errOut.String())
	}
	modBytes, err := os.ReadFile(filepath.Join(tmp, "myapp", "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if !strings.Contains(string(modBytes), "module myapp") {
		t.Errorf("go.mod = %q, want it to declare module myapp (defaulted from the name)", modBytes)
	}
}

func TestRun_New_RefusesNonEmptyDirWithoutForce(t *testing.T) {
	c, _, errOut := testCLI()
	target := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "existing.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	code := c.Run(context.Background(), []string{"new", "demo", "-dir", target})

	if code != 1 {
		t.Fatalf("Run() = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "not empty") {
		t.Errorf("stderr = %q, want it to mention the non-empty directory", errOut.String())
	}
	if _, err := os.Stat(filepath.Join(target, "main.go")); err == nil {
		t.Error("main.go was written despite the refusal")
	}
}

func TestRun_New_ForceOverridesNonEmptyDir(t *testing.T) {
	c, _, errOut := testCLI()
	target := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "existing.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	code := c.Run(context.Background(), []string{"new", "demo", "-dir", target, "-force"})

	if code != 0 {
		t.Fatalf("Run() = %d, want 0; stderr = %s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(target, "main.go")); err != nil {
		t.Errorf("main.go was not written: %v", err)
	}
}

func TestRun_New_ScaffoldsExpectedFiles(t *testing.T) {
	c, _, errOut := testCLI()
	target := filepath.Join(t.TempDir(), "proj")

	code := c.Run(context.Background(), []string{"new", "demo", "-dir", target})

	if code != 0 {
		t.Fatalf("Run() = %d, want 0; stderr = %s", code, errOut.String())
	}

	for _, want := range []string{
		"go.mod",
		"main.go",
		"main_test.go",
		"README.md",
		".gitignore",
		".env.example",
		"plugins-config.json",
		filepath.Join("pages", "home.go"),
		filepath.Join("fragments", "layouts", "main.go"),
		filepath.Join("templates", "layouts", "default.html"),
		filepath.Join("templates", "pages", "home.html"),
		filepath.Join("static", "app.css"),
	} {
		if _, err := os.Stat(filepath.Join(target, want)); err != nil {
			t.Errorf("expected file %s not written: %v", want, err)
		}
	}

	// The embedded scaffold's own template files are the placeholder-bearing
	// ones; go.mod.tmpl and main.go.tmpl must not survive under their
	// original ".tmpl" names.
	if _, err := os.Stat(filepath.Join(target, "go.mod.tmpl")); err == nil {
		t.Error("go.mod.tmpl was written verbatim; the .tmpl suffix should have been stripped")
	}
}

// TestRun_New_Scaffold_Compiles scaffolds a project into t.TempDir() and
// verifies the result actually builds. Since the scaffolded go.mod has no
// requirement on collage-core (collage new leaves that to "go mod tidy" for a
// real user, once the module is published), this test adds a replace
// directive pointing at this checkout before running "go mod tidy" and
// "go build ./...", rather than letting module resolution fail on a module
// path that is not published anywhere GOPROXY can reach.
func TestRun_New_Scaffold_Compiles(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go binary not available")
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err != nil {
		t.Fatalf("repo root guess %s has no go.mod: %v", repoRoot, err)
	}

	c, _, errOut := testCLI()
	target := filepath.Join(t.TempDir(), "proj")

	code := c.Run(context.Background(), []string{"new", "demo", "-dir", target, "-module", "collagescaffoldtest"})
	if code != 0 {
		t.Fatalf("Run() = %d, want 0; stderr = %s", code, errOut.String())
	}

	runIn := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = target
		cmd.Env = append(os.Environ(), "GOWORK=off")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	runIn(goBin, "mod", "edit", "-replace", "github.com/Elagoht/collage="+repoRoot)
	runIn(goBin, "mod", "tidy")
	runIn(goBin, "build", "./...")

	// The scaffold ships tests, and they have to pass — otherwise a new project
	// starts with a red suite, which is worse than starting with none. Running
	// them here is also what keeps them honest as the framework changes: they
	// drive app.Handler() through the real pages, the real form and the real
	// forgery check, so a change that breaks any of those breaks this.
	runIn(goBin, "test", "./...")

	// The scaffold's own -collage-build path is not a hand-rolled writer: it
	// calls collage.NewBuilder, the same static builder collage-core's own
	// internal/build implements. Running it for real here, and checking the
	// file it wrote, is the check that "collage build" reaches that real
	// builder rather than some weaker stand-in the scaffold carries on its
	// own — see docs/plans/collage-core.md's "re-export the static builder"
	// amendment for why this distinction matters.
	// Eight files: the home page's index.html, the 404 page, and each of the
	// three mounted static files under its own name and under the
	// content-addressed name the layout links through {{asset}}. A mount is
	// copied into the build output by default, so a scaffolded project's static
	// files are in dist/ without any further wiring.
	buildOut := runIn(goBin, "run", ".", "-collage-build", "-out", "dist")
	if !strings.Contains(buildOut, "8 files written") {
		t.Fatalf("build output = %q, want it to report eight files written", buildOut)
	}
	// Three skips, and naming them is the point: the features page, because its
	// forms need a server to submit to; the hello page, because it is Dynamic();
	// and the health check, because what it reports is this process being up
	// rather than something cached from when it was.
	if !strings.Contains(buildOut, "3 pages skipped") {
		t.Errorf("build output = %q, want it to count the skips", buildOut)
	}
	// The not-found page is in the output as 404.html, so reporting it as
	// skipped would be saying the opposite of what happened.
	if strings.Contains(buildOut, "not-found") {
		t.Errorf("build output = %q, want the not-found page not reported as skipped", buildOut)
	}
	if !strings.Contains(buildOut, "404.html") {
		t.Errorf("build output = %q, want it to have written a 404 page", buildOut)
	}
	for _, name := range []string{"features", "hello", "health"} {
		if !strings.Contains(buildOut, name) {
			t.Errorf("build output = %q, want it to name the skipped %q", buildOut, name)
		}
	}
	// The last line is the one people read, so it has to carry the counts.
	if !strings.Contains(buildOut, "8 written · 3 skipped · 0 failed") {
		t.Errorf("build output = %q, want a summary line carrying every count", buildOut)
	}

	stylesheetPath := filepath.Join(target, "dist", "static", "app.css")
	if _, err := os.Stat(stylesheetPath); err != nil {
		t.Fatalf("the mounted stylesheet was not copied into the build output: %v", err)
	}

	// The fingerprinted copy is what index.html links, so it has to exist as a
	// file: a built site is served by a plain file server, and nothing there
	// can strip a hash the way the mount does.
	fingerprinted, err := filepath.Glob(filepath.Join(target, "dist", "static", "app.*.css"))
	if err != nil || len(fingerprinted) != 1 {
		t.Fatalf("content-addressed stylesheet = %v (err %v), want exactly one", fingerprinted, err)
	}

	indexPath := filepath.Join(target, "dist", "index.html")
	body, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read %s: %v", indexPath, err)
	}
	if len(body) == 0 {
		t.Fatal("dist/index.html is empty")
	}

	// -clean must reach BuildOptions.Clean, not just be accepted and ignored:
	// write a marker file that only survives if it wasn't cleaned.
	marker := filepath.Join(target, "dist", "stale.txt")
	if err := os.WriteFile(marker, []byte("stale"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	runIn(goBin, "run", ".", "-collage-build", "-out", "dist", "-clean")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("stale.txt survived a -clean build (err = %v), want it removed", err)
	}
}
