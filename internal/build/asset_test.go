package build

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/internal/asset"
)

// TestBuild_CopiesMountedAssets verifies a mount's file is copied into the build
// output at "<OutDir>/<prefix><name>" with the mount's exact bytes.
func TestBuild_CopiesMountedAssets(t *testing.T) {
	out := resolvedTempDir(t)
	fsys := fstest.MapFS{
		"app.css": &fstest.MapFile{Data: []byte("body { color: red; }")},
	}
	mount, err := asset.New("/static/", fsys)
	if err != nil {
		t.Fatalf("asset.New: %v", err)
	}

	app := &fakeRenderer{}
	b, err := New(app, Options{OutDir: out, Mounts: []*asset.Mount{mount}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := filepath.Join(out, "static", "app.css")
	got := readFile(t, want)
	if got != "body { color: red; }" {
		t.Fatalf("content = %q, want the mount's exact bytes", got)
	}

	found := false
	for _, w := range report.Written {
		if w == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("Written = %v, want it to include %s", report.Written, want)
	}
}

// TestBuild_HonoursWithoutBuildCopy verifies a mount registered with
// asset.WithoutBuildCopy is left out of the static build entirely: "<out>/static/"
// must not exist afterwards.
func TestBuild_HonoursWithoutBuildCopy(t *testing.T) {
	out := resolvedTempDir(t)
	fsys := fstest.MapFS{
		"app.css": &fstest.MapFile{Data: []byte("body { color: red; }")},
	}
	mount, err := asset.New("/static/", fsys, asset.WithoutBuildCopy())
	if err != nil {
		t.Fatalf("asset.New: %v", err)
	}

	app := &fakeRenderer{}
	b, err := New(app, Options{OutDir: out, Mounts: []*asset.Mount{mount}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := b.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if _, err := os.Stat(filepath.Join(out, "static")); !os.IsNotExist(err) {
		t.Fatalf("stat <out>/static: err = %v, want not-exist", err)
	}
}

// TestBuild_AssetCopyCannotEscapeOutDir is the required hostile test: a mount's
// fs.FS is user-supplied, exactly as a PathProvider is, so its fs.WalkDir names are
// equally untrusted input. evilAssetFS's single entry is named "../../escape",
// which — joined with the mount's "/static/" prefix — resolves outside OutDir once
// cleaned. The copy must be refused, with nothing written outside OutDir.
func TestBuild_AssetCopyCannotEscapeOutDir(t *testing.T) {
	out := resolvedTempDir(t)
	mount, err := asset.New("/static/", evilAssetFS{})
	if err != nil {
		t.Fatalf("asset.New: %v", err)
	}

	app := &fakeRenderer{}
	b, err := New(app, Options{OutDir: out, Mounts: []*asset.Mount{mount}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, buildErr := b.Build(context.Background())
	if buildErr == nil {
		t.Fatal("Build succeeded for an escaping mount entry, want an error")
	}
	if !errors.Is(buildErr, ErrPathEscapesOutDir) {
		t.Fatalf("err = %v, want ErrPathEscapesOutDir", buildErr)
	}
	if len(report.Written) != 0 {
		t.Fatalf("Written = %v, want none", report.Written)
	}

	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatalf("ReadDir(out): %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("OutDir has unexpected entries: %v", entries)
	}

	// The resolved escape target — one directory up from OutDir, "escape" — must
	// not have been created either.
	escapeTarget := filepath.Join(filepath.Dir(out), "escape")
	if _, err := os.Stat(escapeTarget); err == nil {
		t.Fatal("escape target was written to disk")
	}
}

// evilAssetFS is an fs.FS whose root directory contains exactly one entry, named
// "../../escape" — standing in for a hostile or buggy mount source. It implements
// fs.ReadDirFS directly rather than relying on the generic Open/ReadDir path,
// because fs.WalkDir prefers ReadDirFS when it is available and this test needs to
// control exactly what names WalkDir sees.
type evilAssetFS struct{}

// Open implements fs.FS. Only the mount root, ".", is ever opened here: WalkDir's
// initial fs.Stat(fsys, ".") call needs it, and the malicious leaf entry is never
// opened at all because copyAssets refuses it before attempting to read its bytes.
func (evilAssetFS) Open(name string) (fs.File, error) {
	if name == "." {
		return evilDirFile{}, nil
	}
	return nil, fs.ErrNotExist
}

// ReadDir implements fs.ReadDirFS.
func (evilAssetFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		return nil, fs.ErrNotExist
	}
	return []fs.DirEntry{evilDirEntry{}}, nil
}

var (
	_ fs.FS        = evilAssetFS{}
	_ fs.ReadDirFS = evilAssetFS{}
)

// evilDirFile is the fs.File evilAssetFS.Open(".") returns: just enough of fs.File
// for fs.WalkDir's initial fs.Stat(fsys, ".") call to see a directory.
type evilDirFile struct{}

func (evilDirFile) Stat() (fs.FileInfo, error) { return evilDirInfo{}, nil }
func (evilDirFile) Read([]byte) (int, error)   { return 0, io.EOF }
func (evilDirFile) Close() error               { return nil }

// evilDirInfo is the fs.FileInfo describing evilAssetFS's root as a directory.
type evilDirInfo struct{}

func (evilDirInfo) Name() string       { return "." }
func (evilDirInfo) Size() int64        { return 0 }
func (evilDirInfo) Mode() fs.FileMode  { return fs.ModeDir }
func (evilDirInfo) ModTime() time.Time { return time.Time{} }
func (evilDirInfo) IsDir() bool        { return true }
func (evilDirInfo) Sys() any           { return nil } // any: matches fs.FileInfo's stdlib signature

// evilDirEntry is the sole, adversarially-named fs.DirEntry evilAssetFS's root
// reports: a file (not a directory) named "../../escape".
type evilDirEntry struct{}

func (evilDirEntry) Name() string               { return "../../escape" }
func (evilDirEntry) IsDir() bool                { return false }
func (evilDirEntry) Type() fs.FileMode          { return 0 }
func (evilDirEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrNotExist }
