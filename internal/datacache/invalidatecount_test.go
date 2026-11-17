package datacache

import (
	"context"
	"sync/atomic"
	"testing"
)

// A value stored under two of the tags invalidated is one value, not two:
// Invalidate reports how many it marked, and the entry is the unit.
func TestStore_InvalidateCountsAValueOnceWhateverNumberOfTagsMatch(t *testing.T) {
	s := New(Options{})
	var calls atomic.Int32
	tags := []string{"author:A", "posts"}
	s.Load(context.Background(), "k", 0, tags, counting(&calls, "v"), decodeStr)

	if marked := s.Invalidate(tags); marked != 1 {
		t.Errorf("Invalidate marked = %d, want 1 for one value under two matching tags", marked)
	}
}
