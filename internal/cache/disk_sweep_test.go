package cache

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// age backdates path's modification time.
func age(t *testing.T, path string, by time.Duration) {
	t.Helper()
	old := time.Now().Add(-by)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestDisk_SweepsOldBuildDirectoriesOnly(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, by time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "entry"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		age(t, p, by)
		return p
	}
	oldBuild := mk("0123456789abcdef", 2*time.Hour)
	recentBuild := mk("fedcba9876543210", time.Minute) // another version still writing
	data := mk("data", 48*time.Hour)
	upper := mk("FEDCBA0123456789", 48*time.Hour)
	short := mk("0123456789abcde", 48*time.Hour)
	file := filepath.Join(dir, "0011223344556677")
	if err := os.WriteFile(file, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	age(t, file, 48*time.Hour)

	c := newTestDisk(t, dir, "current", nil)
	age(t, c.Dir(), 48*time.Hour) // the current one is never swept, however old

	_ = newTestDisk(t, dir, "current", nil)

	if exists(oldBuild) {
		t.Error("a build directory untouched for longer than the grace period was kept")
	}
	for name, p := range map[string]string{"recent build": recentBuild, "data": data, "upper-case": upper, "short": short, "file": file, "current": c.Dir()} {
		if !exists(p) {
			t.Errorf("%s was removed; only old build directories are the cache's to remove", name)
		}
	}
}

func TestDisk_SweepsStaleTempFilesOnly(t *testing.T) {
	dir := t.TempDir()
	c := newTestDisk(t, dir, "v", nil)
	crashed := filepath.Join(c.Dir(), ".tmp-crashed")
	inflight := filepath.Join(c.Dir(), ".tmp-inflight")
	for _, p := range []string{crashed, inflight} {
		if err := os.WriteFile(p, []byte("half"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	age(t, crashed, time.Hour)

	_ = newTestDisk(t, dir, "v", nil)
	if exists(crashed) {
		t.Error("a temporary file left by a crash was kept")
	}
	if !exists(inflight) {
		t.Error("a fresh temporary file, possibly another process's write in flight, was removed")
	}
}

func TestDisk_ATruncatedEntryIsAMiss(t *testing.T) {
	// A cut file still parses: the header is whole and the content is just
	// shorter. Served as a hit it would carry the original ETag, and a client
	// holding it would get a 304 for a body it never received whole.
	ctx := context.Background()
	c := newTestDisk(t, t.TempDir(), "v", nil)
	if _, err := c.Set(ctx, "k", []byte("<html>a page that is long enough to cut</html>"), time.Minute); err != nil {
		t.Fatal(err)
	}
	path, _ := c.path("k")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, info.Size()-10); err != nil {
		t.Fatal(err)
	}
	if _, _, found := c.Get(ctx, "k"); found {
		t.Error("Get served a truncated entry")
	}
	if _, _, _, found := c.GetStale(ctx, "k"); found {
		t.Error("GetStale served a truncated entry")
	}
	if _, _, found := c.load("k"); found {
		t.Error("load would warm a truncated entry into memory")
	}
}
