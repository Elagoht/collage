package cache

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Next.js: test/e2e/incremental-cache-path-traversal ("serves arbitrary json
// files"), where a route parameter of "..%2F..%2Fserver-reference-manifest" became
// part of an incremental-cache file path and read the build's secrets. A disk
// cache key or version names no path: whatever either holds, every entry is
// written inside the cache's own directory, and no key reads a file that the
// cache did not write.
func TestNextjs_ADiskCacheKeyNamesNoPath(t *testing.T) {
	root := t.TempDir()
	cacheDir := filepath.Join(root, "cache")
	if err := os.WriteFile(filepath.Join(root, "server-reference-manifest.json"), []byte(`{"encryptionKey":"SECRET"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	for _, version := range []string{"v1", "../../escape", `..\..\escape`, "/abs"} {
		c := newTestDisk(t, cacheDir, version, nil)
		if rel, err := filepath.Rel(cacheDir, c.Dir()); err != nil || strings.HasPrefix(rel, "..") || strings.ContainsRune(rel, filepath.Separator) {
			t.Errorf("version %q: entries live at %s, outside %s", version, c.Dir(), cacheDir)
		}

		for _, key := range []string{
			"../server-reference-manifest.json", "../../server-reference-manifest.json",
			`..\..\server-reference-manifest.json`, filepath.Join(root, "server-reference-manifest.json"),
			"a/../../../server-reference-manifest.json", "..%2F..%2Fserver-reference-manifest",
		} {
			if content, _, found := c.Get(ctx, key); found || strings.Contains(string(content), "SECRET") {
				t.Errorf("version %q: Get(%q) = %q, %v before anything was stored", version, key, content, found)
			}
			if _, err := c.Set(ctx, key, []byte("stored"), time.Minute); err != nil {
				t.Fatalf("Set(%q): %v", key, err)
			}
			if content, _, found := c.Get(ctx, key); !found || string(content) != "stored" {
				t.Errorf("version %q: Get(%q) = %q, %v, want what was stored", version, key, content, found)
			}
		}
	}

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if path == filepath.Join(root, "server-reference-manifest.json") {
			if data, _ := os.ReadFile(path); !strings.Contains(string(data), "SECRET") {
				t.Errorf("the file outside the cache was overwritten: %q", data)
			}
			return nil
		}
		if rel, _ := filepath.Rel(cacheDir, path); strings.HasPrefix(rel, "..") {
			t.Errorf("an entry was written outside the cache directory: %s", path)
		}
		return nil
	})
}
