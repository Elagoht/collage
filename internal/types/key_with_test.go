package types

import "testing"

// With joins parts with a colon, so a part holding one used to be
// indistinguishable from two parts: slugs "b:c" and ("b", "c") fetched, and
// shared, one value. Cached keys outlive the render, so the collision was served
// across requests.
func TestKey_WithKeepsPartsApart(t *testing.T) {
	joined := NewKey[string]("a").With("b:c")
	split := NewKey[string]("a:b").With("c")
	if joined.Name() == split.Name() {
		t.Fatalf("a.With(%q) and %q.With(%q) are one key, %q", "b:c", "a:b", "c", joined.Name())
	}
	if NewKey[string]("a").With("b").With("c").Name() == joined.Name() {
		t.Error("two parts and one part holding a colon are one key")
	}
	if NewKey[string]("a").With(`b\`).With("c").Name() == joined.Name() {
		t.Error("an escaped colon and a part ending in a backslash are one key")
	}
}

// What keys without a colon in their parts are named is what a data cache may
// have written to disk, so it does not change.
func TestKey_WithPlainPartsKeepTheirName(t *testing.T) {
	if got := NewKey[string]("article").With("my-post_1").With("en").Name(); got != "article:my-post_1:en" {
		t.Errorf("Name = %q", got)
	}
}
