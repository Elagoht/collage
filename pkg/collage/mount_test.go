package collage

import (
	"testing"
	"testing/fstest"
)

// TestMounts_ElementTypeHasAPublicName declares a []*Mount —
// the exact type the application's mounts are listed as — and reads its methods
// off an element, using only pkg/collage's names.
//
// App no longer exposes Mounts (a static build is the one reader, through
// Build), so this test lives inside the package and reads the list through the
// application it wraps. Mount stays exported: what the test guards is that the
// element type has a pkg/collage name, so the build's view of a mount never
// needs internal/asset spelled out.
func TestMounts_ElementTypeHasAPublicName(t *testing.T) {
	app, err := New(&Config{
		Server:   ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{Root: templateRoot(t)},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = app.Mount("/static/", fstest.MapFS{
		"app.css": {Data: []byte("body{color:red}")},
	}, WithCacheControl("public, max-age=60"))
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	app.Handler() // closes registration; Mounts is readable either side of this.

	var mounts []*Mount = app.c.Mounts()
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
