package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// cleanBuild runs a Clean build of an empty app into outDir.
func cleanBuild(t *testing.T, outDir string) error {
	t.Helper()
	b, err := New(&fakeRenderer{}, Options{OutDir: outDir, Clean: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = b.Build(context.Background())
	return err
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBuild_Clean_RefusesAncestorOfWorkingDirectory is the "export -out .. -clean"
// case: the parent of the project is not itself a repository root, so the marker
// check let it through and emptied the project with everything beside it.
func TestBuild_Clean_RefusesAncestorOfWorkingDirectory(t *testing.T) {
	parent := resolvedTempDir(t)
	proj := filepath.Join(parent, "proj")
	writeTestFile(t, filepath.Join(proj, "go.mod"), "module x\n")
	writeTestFile(t, filepath.Join(parent, "sibling", "keep.txt"), "keep")
	t.Chdir(proj)

	for _, out := range []string{"..", ".", parent} {
		if err := cleanBuild(t, out); !errors.Is(err, ErrDangerousOutDir) {
			t.Fatalf("Clean into %q: err = %v, want ErrDangerousOutDir", out, err)
		}
	}
	for _, p := range []string{filepath.Join(proj, "go.mod"), filepath.Join(parent, "sibling", "keep.txt")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s was deleted: %v", p, err)
		}
	}
}

// TestBuild_Clean_RefusesAncestorOfModuleRoot covers a program started from a
// subdirectory of its module: the module root is above the working directory and
// is just as much the user's own tree.
func TestBuild_Clean_RefusesAncestorOfModuleRoot(t *testing.T) {
	parent := resolvedTempDir(t)
	root := filepath.Join(parent, "proj")
	writeTestFile(t, filepath.Join(root, "go.mod"), "module x\n")
	cmdDir := filepath.Join(root, "cmd", "app")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cmdDir)

	// Beside the module root, not under it: only the module-root ancestry check
	// can see that this directory holds the whole project.
	if err := cleanBuild(t, parent); !errors.Is(err, ErrDangerousOutDir) {
		t.Fatalf("err = %v, want ErrDangerousOutDir", err)
	}
}

// TestBuild_Clean_RefusesDirectoryItDidNotExport is "export -out static -clean":
// a source directory of the project is a perfectly ordinary directory, and the
// only thing that tells it apart from a previous export is that collage wrote the
// export and left its marker there.
func TestBuild_Clean_RefusesDirectoryItDidNotExport(t *testing.T) {
	out := filepath.Join(resolvedTempDir(t), "static")
	css := filepath.Join(out, "app.css")
	writeTestFile(t, css, "body{}")

	if err := cleanBuild(t, out); !errors.Is(err, ErrDangerousOutDir) {
		t.Fatalf("err = %v, want ErrDangerousOutDir", err)
	}
	if _, err := os.Stat(css); err != nil {
		t.Fatalf("app.css was deleted: %v", err)
	}
}

// TestBuild_Clean_ReExportsIntoItsOwnOutput is the normal case that must stay
// frictionless: a first build into a missing directory, then -clean over it.
func TestBuild_Clean_ReExportsIntoItsOwnOutput(t *testing.T) {
	out := filepath.Join(resolvedTempDir(t), "dist")
	page := newTestPage("about", types.StrategyStatic, map[string]string{"en": "/about"})

	build := func() {
		t.Helper()
		b, err := New(&fakeRenderer{pages: []*types.Page{page}}, Options{OutDir: out, Clean: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := b.Build(context.Background()); err != nil {
			t.Fatalf("Build: %v", err)
		}
	}
	build()
	stale := filepath.Join(out, "stale.html")
	writeTestFile(t, stale, "old")
	build()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale file survived a second Clean build: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "about", "index.html")); err != nil {
		t.Fatal(err)
	}
}

// TestBuild_NoClean_DoesNotMarkADirectoryItDidNotCreate keeps the marker honest:
// exporting without -clean into someone's populated directory must not be what
// later lets -clean empty it.
func TestBuild_NoClean_DoesNotMarkADirectoryItDidNotCreate(t *testing.T) {
	out := filepath.Join(resolvedTempDir(t), "static")
	writeTestFile(t, filepath.Join(out, "app.css"), "body{}")

	b, err := New(&fakeRenderer{}, Options{OutDir: out})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := cleanBuild(t, out); !errors.Is(err, ErrDangerousOutDir) {
		t.Fatalf("Clean after a plain build: err = %v, want ErrDangerousOutDir", err)
	}
}
