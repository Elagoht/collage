package asset

import (
	"io/fs"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

// countingFS counts opens and holds each one long enough for concurrent callers
// to pile up behind it.
type countingFS struct {
	fstest.MapFS
	opens atomic.Int32
}

func (c *countingFS) Open(name string) (fs.File, error) {
	c.opens.Add(1)
	time.Sleep(50 * time.Millisecond)
	return c.MapFS.Open(name)
}

// A cold file requested by many clients at once was hashed once per client, each
// reading the whole file; the first request should hash it and the rest wait for
// that answer.
func TestETag_ConcurrentFirstRequestsHashOnce(t *testing.T) {
	fsys := &countingFS{MapFS: fstest.MapFS{"big.bin": {Data: []byte("payload")}}}
	tags := newETagCache()

	var wg sync.WaitGroup
	results := make([]string, 20)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = tags.get(fsys, "big.bin")
		}()
	}
	wg.Wait()

	if got := fsys.opens.Load(); got != 1 {
		t.Errorf("file opened %d times for 20 concurrent first requests, want 1", got)
	}
	for i, tag := range results {
		if tag == "" || tag != results[0] {
			t.Fatalf("result %d = %q, want every caller to get %q", i, tag, results[0])
		}
	}
}

// Development mode re-hashes on every call by design, and does not share a hash
// between callers either: an edit between two of them must show in the second.
func TestETag_WithoutMemoisingEveryCallHashes(t *testing.T) {
	fsys := &countingFS{MapFS: fstest.MapFS{"a.css": {Data: []byte("x")}}}
	tags := newETagCache()
	for range 2 {
		if _, err := tags.lookup(fsys, "a.css", false); err != nil {
			t.Fatal(err)
		}
	}
	if got := fsys.opens.Load(); got != 2 {
		t.Errorf("opens = %d, want 2", got)
	}
}
