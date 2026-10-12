package collage

import (
	"testing"
	"testing/fstest"
)

// TestMounts_ReportPrefixCopyAndFS reads the mounts off the application a
// wrapped App holds. App does not expose Mounts (a static build is the one
// reader, through Build), so the test lives inside the package.
func TestMounts_ReportPrefixCopyAndFS(t *testing.T) {
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

	mounts := app.c.Mounts()
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
