package dependency

import (
	"context"
	"testing"
)

// A key a cap drops is one an invalidation of its tag can no longer reach, so the
// owner of the cache is told, to drop the entry it still holds.
func TestMemoryTracker_OnEvictNamesEveryKeyACapDropped(t *testing.T) {
	ctx := context.Background()

	t.Run("MaxKeys", func(t *testing.T) {
		tr := NewMemory()
		tr.MaxKeys = 2
		var got []string
		tr.OnEvict = func(keys []string) { got = append(got, keys...) }
		for _, key := range []string{"a", "b", "c", "d"} {
			if err := tr.Track(ctx, key, []string{"t"}); err != nil {
				t.Fatal(err)
			}
		}
		if !sameStrings(got, []string{"a", "b"}) {
			t.Fatalf("evicted = %v, want [a b]", got)
		}
	})

	t.Run("MaxKeysPerTag", func(t *testing.T) {
		tr := NewMemory()
		tr.MaxKeysPerTag = 2
		var got []string
		tr.OnEvict = func(keys []string) { got = append(got, keys...) }
		// "a" stays tracked under "other", but an invalidation of "t" no longer
		// reaches it, which is what the callback is for.
		for _, key := range []string{"a", "b", "c"} {
			if err := tr.Track(ctx, key, []string{"t", "other"}); err != nil {
				t.Fatal(err)
			}
		}
		if !sameStrings(got, []string{"a"}) {
			t.Fatalf("evicted = %v, want [a]", got)
		}
	})

	t.Run("called with no lock held", func(t *testing.T) {
		tr := NewMemory()
		tr.MaxKeys = 1
		tr.OnEvict = func([]string) {
			// Would deadlock if Track still held its lock.
			if _, err := tr.Resolve(ctx, []string{"t"}); err != nil {
				t.Error(err)
			}
		}
		_ = tr.Track(ctx, "a", []string{"t"})
		_ = tr.Track(ctx, "b", []string{"t"})
	})
}
