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
		"README.md",
		".gitignore",
		filepath.Join("templates", "layouts", "default.html"),
		filepath.Join("templates", "pages", "home.html"),
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

	// The scaffold's own -collage-build path is not a hand-rolled writer: it
	// calls collage.NewBuilder, the same static builder collage-core's own
	// internal/build implements. Running it for real here, and checking the
	// file it wrote, is the check that "collage build" reaches that real
	// builder rather than some weaker stand-in the scaffold carries on its
	// own — see docs/plans/collage-core.md's "re-export the static builder"
	// amendment for why this distinction matters.
	buildOut := runIn(goBin, "run", ".", "-collage-build", "-out", "dist")
	if !strings.Contains(buildOut, "build complete, 1 file(s) written") {
		t.Fatalf("build output = %q, want it to report exactly one file written", buildOut)
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
