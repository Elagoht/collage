package core

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/render"
)

func memoResult(html string, tags ...string) func(context.Context) (*render.FragmentResult, error) {
	return func(context.Context) (*render.FragmentResult, error) {
		return &render.FragmentResult{HTML: []byte(html), DependencyTags: tags}, nil
	}
}

// The memo holds at most maxEntries renders and maxBytes of them, dropping the
// least recently used first, and forgets a render past its age.
func TestFragmentMemo_Bounds(t *testing.T) {
	ctx := context.Background()
	m := newFragmentMemo(0)
	m.maxEntries = 3
	for i := range 3 {
		if _, err := m.get(ctx, fmt.Sprint(i), 0, nil, memoResult(fmt.Sprint("r", i))); err != nil {
			t.Fatal(err)
		}
	}
	// 0 used again: 1 is the least recently used when 3 comes in.
	if _, err := m.get(ctx, "0", 0, nil, memoResult("fresh")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.get(ctx, "3", 0, nil, memoResult("r3")); err != nil {
		t.Fatal(err)
	}
	if len(m.entries) != 3 || m.entries["1"] != nil || m.entries["0"] == nil {
		t.Errorf("after a fourth render: %d entries, 1 kept %v, 0 kept %v", len(m.entries), m.entries["1"] != nil, m.entries["0"] != nil)
	}

	m = newFragmentMemo(10)
	m.get(ctx, "a", 0, nil, memoResult("12345"))
	m.get(ctx, "b", 0, nil, memoResult("12345"))
	m.get(ctx, "c", 0, nil, memoResult("12345"))
	m.get(ctx, "huge", 0, nil, memoResult("12345678901"))
	if m.bytes > 10 || m.entries["a"] != nil || m.entries["huge"] != nil || m.entries["c"] == nil {
		t.Errorf("a 10-byte memo holds %d bytes: %v", m.bytes, len(m.entries))
	}

	m = newFragmentMemo(0)
	now := time.Unix(0, 0)
	m.now = func() time.Time { return now }
	m.get(ctx, "k", time.Second, nil, memoResult("old"))
	now = now.Add(2 * time.Second)
	got, _ := m.get(ctx, "k", time.Second, nil, memoResult("new"))
	if string(got.HTML) != "new" {
		t.Errorf("past its age the memo answered %q", got.HTML)
	}
}

// drop reaches an entry by its own tags and by those it was kept under.
func TestFragmentMemo_DropByTag(t *testing.T) {
	ctx := context.Background()
	m := newFragmentMemo(0)
	m.get(ctx, "a", 0, []string{"path:/a"}, memoResult("a", "posts"))
	m.get(ctx, "b", 0, []string{"path:/b"}, memoResult("b", "users"))
	m.drop([]string{"posts"})
	m.drop([]string{"path:/b"})
	if len(m.entries) != 0 || len(m.byTag) != 0 || m.head != nil || m.tail != nil || m.bytes != 0 {
		t.Errorf("after dropping both: %d entries, %d tags, %d bytes", len(m.entries), len(m.byTag), m.bytes)
	}
}
