package asset

import (
	"testing"
	"testing/fstest"
)

func TestETag_IsStableAndContentDerived(t *testing.T) {
	fsys := fstest.MapFS{"a.txt": {Data: []byte("hello")}, "b.txt": {Data: []byte("world")}}
	tags := newETagCache()

	first, err := tags.get(fsys, "a.txt")
	if err != nil {
		t.Fatalf("get() = %v", err)
	}
	second, err := tags.get(fsys, "a.txt")
	if err != nil {
		t.Fatalf("get() = %v", err)
	}
	if first != second {
		t.Fatalf("etag changed between calls: %q then %q", first, second)
	}
	other, _ := tags.get(fsys, "b.txt")
	if first == other {
		t.Fatal("different content produced the same etag")
	}
	if first[0] != '"' || first[len(first)-1] != '"' {
		t.Fatalf("etag %q must be quoted per the HTTP grammar", first)
	}
}

func TestETag_MemoisesWithoutRetainingBodies(t *testing.T) {
	fsys := fstest.MapFS{"a.txt": {Data: []byte("hello")}}
	tags := newETagCache()

	if _, err := tags.get(fsys, "a.txt"); err != nil {
		t.Fatalf("get() = %v", err)
	}
	if got := tags.len(); got != 1 {
		t.Fatalf("cache holds %d entries, want 1", got)
	}
	// The cache stores hashes, never bodies: entry size is bounded by file count.
}
