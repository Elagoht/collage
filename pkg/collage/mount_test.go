// Package collage_test proves, from entirely outside package collage, that
// App.Mounts()'s return type is fully usable with nothing but the standard
// library and pkg/collage imported. It is a separate, external test package
// (not "package collage") specifically so that its import list is the proof:
// if Mount's element type needed internal/asset to be named, this file could
// not compile without importing it, and internal/asset is unreachable from
// outside this module.
package collage_test

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// mountTemplateRoot writes the minimal template set an App needs to start, and
// returns its directory. It is this file's own copy of collage_test.go's (the
// internal test package's) templateRoot: an external test package cannot reach
// an internal test helper of the package under test.
func mountTemplateRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	path := filepath.Join(root, "pages", "home.html")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`<h1>Welcome Home</h1>`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return root
}

// TestMounts_ElementTypeIsNameableFromOutsideThePackage declares a
// []*collage.Mount — the exact type App.Mounts() returns, through the
// App = core.App alias — and reads two of its methods off an element, using
// only pkg/collage and the standard library.
//
// Before the Mount alias existed, App.Mounts() could still be *called* from
// here, but its element type could not be *named*: internal/asset, where
// asset.Mount actually lives, is unreachable from outside this module, so a
// caller here could declare no variable, slice, or function parameter of that
// type. That is the same defect App.RenderPath's Result alias exists to avoid
// for *render.Result — Global Constraint 7 is what this test is standing in
// for.
func TestMounts_ElementTypeIsNameableFromOutsideThePackage(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{Root: mountTemplateRoot(t)},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = app.Mount("/static/", fstest.MapFS{
		"app.css": {Data: []byte("body{color:red}")},
	}, collage.WithCacheControl("public, max-age=60"))
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	app.Handler() // closes registration; Mounts is readable either side of this.

	var mounts []*collage.Mount = app.Mounts()
	if len(mounts) != 1 {
		t.Fatalf("Mounts() = %d entries, want 1", len(mounts))
	}

	mount := mounts[0]
	if got := mount.Prefix(); got != "/static/" {
		t.Fatalf("Prefix() = %q, want %q", got, "/static/")
	}
	if !mount.BuildCopy() {
		t.Fatal("BuildCopy() = false, want true (the default)")
	}
	if mount.FS() == nil {
		t.Fatal("FS() = nil, want the mounted file system")
	}
}
