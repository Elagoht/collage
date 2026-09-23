package core

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestTemplateSource_ProductionAlwaysUsesTheEmbeddedCopy(t *testing.T) {
	// Even standing in a directory where the templates exist: a production binary
	// renders what it was built with, or it is not a production binary.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	embedded := fstest.MapFS{"templates/page.html": &fstest.MapFile{Data: []byte("x")}}
	if got := templateSource(embedded, "templates", false, discardLogger()); got == nil {
		t.Error("templateSource() = nil in production, want the embedded filesystem")
	}
}

func TestTemplateSource_DevPrefersTheDirectoryOnDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	embedded := fstest.MapFS{"templates/page.html": &fstest.MapFile{Data: []byte("x")}}
	if got := templateSource(embedded, "templates", true, discardLogger()); got != nil {
		t.Error("templateSource() returned the embedded filesystem; an edit to a file on disk would never be seen")
	}
}

func TestTemplateSource_DevFallsBackWhenTheDirectoryIsNotThere(t *testing.T) {
	t.Chdir(t.TempDir())

	embedded := fstest.MapFS{"templates/page.html": &fstest.MapFile{Data: []byte("x")}}
	got := templateSource(embedded, "templates", true, discardLogger())
	if got == nil {
		t.Fatal("templateSource() = nil with no directory on disk, want the embedded filesystem")
	}
	if _, err := fs.Stat(got, "templates/page.html"); err != nil {
		t.Errorf("the returned filesystem is not the embedded one: %v", err)
	}
}

// A file where the directory should be is not a template root.
func TestTemplateSource_DevIgnoresAFileOfThatName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "templates"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	embedded := fstest.MapFS{"templates/page.html": &fstest.MapFile{Data: []byte("x")}}
	if got := templateSource(embedded, "templates", true, discardLogger()); got == nil {
		t.Error("templateSource() = nil for a plain file named like the root, want the embedded filesystem")
	}
}

// An application that never embedded anything is already reading from disk.
func TestTemplateSource_NoEmbeddedCopy(t *testing.T) {
	if got := templateSource(nil, "templates", true, discardLogger()); got != nil {
		t.Error("templateSource() invented a filesystem where the application supplied none")
	}
}
