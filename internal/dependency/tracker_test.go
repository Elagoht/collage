package dependency

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
)

// sameStrings reports whether a and b hold the same strings in the same order,
// treating nil and an empty non-nil slice as equal so tests don't have to care
// which one an implementation detail happens to return.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// trackCall is one Track invocation used to seed a MemoryTracker in table-driven
// tests below.
type trackCall struct {
	key  string
	tags []string
}

func TestMemoryTracker_TrackAndResolve(t *testing.T) {
	tests := []struct {
		name    string
		tracks  []trackCall
		resolve []string
		want    []string
	}{
		{
			name:    "round trip single tag",
			tracks:  []trackCall{{key: "k1", tags: []string{"a"}}},
			resolve: []string{"a"},
			want:    []string{"k1"},
		},
		{
			name:    "multi-tag key resolves from either tag it belongs to",
			tracks:  []trackCall{{key: "k1", tags: []string{"a", "b"}}},
			resolve: []string{"b"},
			want:    []string{"k1"},
		},
		{
			name: "multiple keys sharing a tag all resolve",
			tracks: []trackCall{
				{key: "k1", tags: []string{"a"}},
				{key: "k2", tags: []string{"a"}},
			},
			resolve: []string{"a"},
			want:    []string{"k1", "k2"},
		},
		{
			name: "resolve unions across requested tags, de-duplicated",
			tracks: []trackCall{
				{key: "k1", tags: []string{"a", "b"}},
				{key: "k2", tags: []string{"b"}},
			},
			resolve: []string{"a", "b"},
			want:    []string{"k1", "k2"},
		},
		{
			name:    "unknown tag resolves empty, not an error",
			tracks:  []trackCall{{key: "k1", tags: []string{"a"}}},
			resolve: []string{"nonexistent"},
			want:    nil,
		},
		{
			name:    "empty tags list resolves empty, not an error",
			tracks:  []trackCall{{key: "k1", tags: []string{"a"}}},
			resolve: []string{},
			want:    nil,
		},
		{
			name:    "empty tags list on Track leaves key with no tags",
			tracks:  []trackCall{{key: "k1", tags: []string{}}},
			resolve: []string{"a"},
			want:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tr := NewMemory()
			for _, c := range tc.tracks {
				if err := tr.Track(ctx, c.key, c.tags); err != nil {
					t.Fatalf("Track(%q, %v) error = %v", c.key, c.tags, err)
				}
			}

			got, err := tr.Resolve(ctx, tc.resolve)
			if err != nil {
				t.Fatalf("Resolve(%v) error = %v", tc.resolve, err)
			}
			if !sameStrings(got, tc.want) {
				t.Errorf("Resolve(%v) = %v, want %v", tc.resolve, got, tc.want)
			}
		})
	}
}

// TestMemoryTracker_RetrackNarrowsTagSet exercises the property the brief calls out
// explicitly: re-tracking a key under a different tag set must replace, not union
// with, its previous tags. Get this wrong and the key either keeps resolving from a
// tag it no longer belongs to, or a stale page never invalidates.
func TestMemoryTracker_RetrackNarrowsTagSet(t *testing.T) {
	ctx := context.Background()
	tr := NewMemory()

	if err := tr.Track(ctx, "k", []string{"a", "b"}); err != nil {
		t.Fatalf("Track(k, [a b]) error = %v", err)
	}
	if err := tr.Track(ctx, "k", []string{"b", "c"}); err != nil {
		t.Fatalf("Track(k, [b c]) error = %v", err)
	}

	if got, err := tr.Resolve(ctx, []string{"a"}); err != nil {
		t.Fatalf("Resolve(a) error = %v", err)
	} else if len(got) != 0 {
		t.Errorf("Resolve(a) = %v, want empty: k must no longer resolve from a tag it was re-tracked away from", got)
	}

	if got, err := tr.Resolve(ctx, []string{"b"}); err != nil {
		t.Fatalf("Resolve(b) error = %v", err)
	} else if !sameStrings(got, []string{"k"}) {
		t.Errorf("Resolve(b) = %v, want [k]", got)
	}

	if got, err := tr.Resolve(ctx, []string{"c"}); err != nil {
		t.Fatalf("Resolve(c) error = %v", err)
	} else if !sameStrings(got, []string{"k"}) {
		t.Errorf("Resolve(c) = %v, want [k]", got)
	}

	if got, err := tr.Tags(ctx, "k"); err != nil {
		t.Fatalf("Tags(k) error = %v", err)
	} else if !sameStrings(got, []string{"b", "c"}) {
		t.Errorf("Tags(k) = %v, want [b c]", got)
	}
}

func TestMemoryTracker_Forget(t *testing.T) {
	ctx := context.Background()
	tr := NewMemory()

	mustTrack(t, tr, "k1", "a", "b")
	mustTrack(t, tr, "k2", "a")

	if err := tr.Forget(ctx, "k1"); err != nil {
		t.Fatalf("Forget(k1) error = %v", err)
	}

	if got, _ := tr.Resolve(ctx, []string{"a"}); !sameStrings(got, []string{"k2"}) {
		t.Errorf("Resolve(a) after Forget(k1) = %v, want [k2]", got)
	}
	if got, _ := tr.Resolve(ctx, []string{"b"}); len(got) != 0 {
		t.Errorf("Resolve(b) after Forget(k1) = %v, want empty: k1 was b's only key", got)
	}
	if got, _ := tr.Tags(ctx, "k1"); len(got) != 0 {
		t.Errorf("Tags(k1) after Forget = %v, want empty", got)
	}

	if err := tr.Forget(ctx, "never-tracked"); err != nil {
		t.Errorf("Forget(never-tracked) error = %v, want nil (forgetting an untracked key is not an error)", err)
	}
}

func TestMemoryTracker_ForgetTags(t *testing.T) {
	ctx := context.Background()
	tr := NewMemory()

	mustTrack(t, tr, "k1", "a", "b")
	mustTrack(t, tr, "k2", "b")

	if err := tr.ForgetTags(ctx, []string{"b"}); err != nil {
		t.Fatalf("ForgetTags([b]) error = %v", err)
	}

	if got, _ := tr.Resolve(ctx, []string{"a"}); !sameStrings(got, []string{"k1"}) {
		t.Errorf("Resolve(a) after ForgetTags(b) = %v, want [k1]: k1's other tag must survive", got)
	}
	if got, _ := tr.Resolve(ctx, []string{"b"}); len(got) != 0 {
		t.Errorf("Resolve(b) after ForgetTags(b) = %v, want empty", got)
	}
	if got, _ := tr.Tags(ctx, "k2"); len(got) != 0 {
		t.Errorf("Tags(k2) after ForgetTags(b) = %v, want empty: k2's only tag was forgotten", got)
	}

	if err := tr.ForgetTags(ctx, []string{"nonexistent"}); err != nil {
		t.Errorf("ForgetTags([nonexistent]) error = %v, want nil", err)
	}
	if err := tr.ForgetTags(ctx, nil); err != nil {
		t.Errorf("ForgetTags(nil) error = %v, want nil", err)
	}
}

// TestMemoryTracker_ResolveDeterministic asserts Resolve returns the same
// de-duplicated, sorted result across repeated calls on unchanged state — Go's
// randomized map iteration order would otherwise make this flaky.
func TestMemoryTracker_ResolveDeterministic(t *testing.T) {
	ctx := context.Background()
	tr := NewMemory()

	mustTrack(t, tr, "k3", "a", "b")
	mustTrack(t, tr, "k1", "a")
	mustTrack(t, tr, "k2", "b")
	mustTrack(t, tr, "k5", "a", "b")
	mustTrack(t, tr, "k4", "a")

	want := []string{"k1", "k2", "k3", "k4", "k5"}

	var first []string
	for i := 0; i < 20; i++ {
		got, err := tr.Resolve(ctx, []string{"a", "b"})
		if err != nil {
			t.Fatalf("Resolve() call %d error = %v", i, err)
		}
		if !sort.StringsAreSorted(got) {
			t.Fatalf("Resolve() call %d = %v, not sorted", i, got)
		}
		if i == 0 {
			first = got
			if !sameStrings(first, want) {
				t.Fatalf("Resolve() = %v, want %v", first, want)
			}
			continue
		}
		if !sameStrings(got, first) {
			t.Fatalf("Resolve() call %d = %v, want identical to call 0's result %v", i, got, first)
		}
	}
}

func TestMemoryTracker_MaxKeysPerTagDropsOldest(t *testing.T) {
	ctx := context.Background()
	tr := NewMemory()
	tr.MaxKeysPerTag = 2

	mustTrack(t, tr, "k1", "a")
	mustTrack(t, tr, "k2", "a")
	mustTrack(t, tr, "k3", "a") // over cap: k1 (oldest) is dropped

	if got, _ := tr.Resolve(ctx, []string{"a"}); !sameStrings(got, []string{"k2", "k3"}) {
		t.Errorf("Resolve(a) = %v, want [k2 k3]: oldest key k1 should have been dropped", got)
	}
	if stats := tr.Stats(); stats.Dropped != 1 {
		t.Errorf("Stats().Dropped = %d, want 1", stats.Dropped)
	}
}

// TestMemoryTracker_MaxKeysPerTagPreservesOtherTags asserts that dropping a key from
// one over-capacity tag does not corrupt the reverse map: the dropped key must
// remain resolvable from any other tag it still belongs to.
func TestMemoryTracker_MaxKeysPerTagPreservesOtherTags(t *testing.T) {
	ctx := context.Background()
	tr := NewMemory()
	tr.MaxKeysPerTag = 1

	mustTrack(t, tr, "x", "a", "c")
	mustTrack(t, tr, "y", "a") // over cap on "a": x (oldest on "a") is dropped from "a"

	if got, _ := tr.Resolve(ctx, []string{"a"}); !sameStrings(got, []string{"y"}) {
		t.Errorf("Resolve(a) = %v, want [y]", got)
	}
	if got, _ := tr.Resolve(ctx, []string{"c"}); !sameStrings(got, []string{"x"}) {
		t.Errorf("Resolve(c) = %v, want [x]: dropping x from tag a must not remove it from tag c", got)
	}
	if got, _ := tr.Tags(ctx, "x"); !sameStrings(got, []string{"c"}) {
		t.Errorf("Tags(x) = %v, want [c]", got)
	}
	if stats := tr.Stats(); stats.Dropped != 1 {
		t.Errorf("Stats().Dropped = %d, want 1", stats.Dropped)
	}
}

func TestMemoryTracker_MaxKeysPerTagZeroIsUnlimited(t *testing.T) {
	ctx := context.Background()
	tr := NewMemory() // MaxKeysPerTag left at its zero value

	for i := 0; i < 100; i++ {
		mustTrack(t, tr, fmt.Sprintf("k%03d", i), "a")
	}

	got, err := tr.Resolve(ctx, []string{"a"})
	if err != nil {
		t.Fatalf("Resolve(a) error = %v", err)
	}
	if len(got) != 100 {
		t.Errorf("Resolve(a) returned %d keys, want 100 (MaxKeysPerTag=0 means unlimited)", len(got))
	}
	if stats := tr.Stats(); stats.Dropped != 0 {
		t.Errorf("Stats().Dropped = %d, want 0", stats.Dropped)
	}
}

func TestMemoryTracker_Clear(t *testing.T) {
	ctx := context.Background()
	tr := NewMemory()

	mustTrack(t, tr, "k1", "a")

	if err := tr.Clear(ctx); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}

	if got, _ := tr.Resolve(ctx, []string{"a"}); len(got) != 0 {
		t.Errorf("Resolve(a) after Clear = %v, want empty", got)
	}
	if got, _ := tr.Tags(ctx, "k1"); len(got) != 0 {
		t.Errorf("Tags(k1) after Clear = %v, want empty", got)
	}
	if stats := tr.Stats(); stats.Keys != 0 || stats.Tags != 0 {
		t.Errorf("Stats() after Clear = %+v, want Keys=0 Tags=0", stats)
	}
}

func TestMemoryTracker_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tr := NewMemory()

	if err := tr.Track(ctx, "k", []string{"a"}); !errors.Is(err, context.Canceled) {
		t.Errorf("Track() error = %v, want context.Canceled", err)
	}
	if _, err := tr.Resolve(ctx, []string{"a"}); !errors.Is(err, context.Canceled) {
		t.Errorf("Resolve() error = %v, want context.Canceled", err)
	}
	if err := tr.Forget(ctx, "k"); !errors.Is(err, context.Canceled) {
		t.Errorf("Forget() error = %v, want context.Canceled", err)
	}
	if err := tr.ForgetTags(ctx, []string{"a"}); !errors.Is(err, context.Canceled) {
		t.Errorf("ForgetTags() error = %v, want context.Canceled", err)
	}
	if _, err := tr.Tags(ctx, "k"); !errors.Is(err, context.Canceled) {
		t.Errorf("Tags() error = %v, want context.Canceled", err)
	}
	if err := tr.Clear(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Clear() error = %v, want context.Canceled", err)
	}
}

// TestMemoryTracker_ConcurrentAccess exercises every method concurrently under -race
// to confirm the single sync.RWMutex actually guards both directions of the index
// together, with no partial update visible to another goroutine.
func TestMemoryTracker_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	tr := NewMemory()
	tr.MaxKeysPerTag = 50

	const goroutines = 20
	const perGoroutine = 50

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				key := fmt.Sprintf("k%d-%d", g, i)
				tags := []string{fmt.Sprintf("tag%d", i%5), "shared"}

				if err := tr.Track(ctx, key, tags); err != nil {
					t.Errorf("Track(%q) error = %v", key, err)
				}
				if _, err := tr.Resolve(ctx, tags); err != nil {
					t.Errorf("Resolve(%v) error = %v", tags, err)
				}
				if _, err := tr.Tags(ctx, key); err != nil {
					t.Errorf("Tags(%q) error = %v", key, err)
				}
				if i%10 == 0 {
					if err := tr.Forget(ctx, key); err != nil {
						t.Errorf("Forget(%q) error = %v", key, err)
					}
				}
			}
		}(g)
	}
	wg.Wait()

	if err := tr.ForgetTags(ctx, []string{"tag0", "tag1"}); err != nil {
		t.Fatalf("ForgetTags() error = %v", err)
	}
	_ = tr.Stats()
	if err := tr.Clear(ctx); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
}

// mustTrack is a test helper that calls Track and fails the test immediately on
// error, to keep setup in the tests above to a single readable line per key.
func mustTrack(t *testing.T, tr *MemoryTracker, key string, tags ...string) {
	t.Helper()
	if err := tr.Track(context.Background(), key, tags); err != nil {
		t.Fatalf("Track(%q, %v) error = %v", key, tags, err)
	}
}
