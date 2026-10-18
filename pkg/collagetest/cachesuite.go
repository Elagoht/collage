package collagetest

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

const (
	// suiteTTL is the short lifetime the suites give an entry they expect to see
	// expire.
	suiteTTL = 50 * time.Millisecond
	// suitePoll and suiteWait bound how long a suite waits for that expiry: it
	// polls every suitePoll and fails after suiteWait, so a slow machine does not
	// make a correct store fail and a store that never expires does not hang.
	suitePoll = 10 * time.Millisecond
	suiteWait = 2 * time.Second
)

// CacheSuite checks that a store meets the collage.Cache contract, so an author
// of a store (a Redis one, say) proves it from their own tests:
//
//	func TestMyStore(t *testing.T) {
//		collagetest.CacheSuite(t, func(t *testing.T) collage.Cache {
//			return newMyStore(t)
//		})
//	}
//
// It runs a "Cache" section always, a "Tagged" section when the store also
// implements collage.TaggedCache and a "Stale" section when it implements
// collage.StaleCache. Each subtest calls newCache for a fresh, empty store, and
// a failure message names the rule that broke. TTL checks use a 50ms TTL and
// poll for up to two seconds, so the store's own default TTL does not matter.
func CacheSuite(t *testing.T, newCache func(t *testing.T) collage.Cache) {
	t.Helper()
	probe := newCache(t)
	_, tagged := probe.(collage.TaggedCache)
	_, stale := probe.(collage.StaleCache)

	t.Run("Cache", func(t *testing.T) {
		suiteRun(t, "RoundTrip", newCache, cacheRoundTrip)
		suiteRun(t, "NotAliased", newCache, cacheNotAliased)
		suiteRun(t, "TTLExpires", newCache, cacheTTLExpires)
		suiteRun(t, "InvalidateKey", newCache, cacheInvalidateKey)
		suiteRun(t, "InvalidateMissingKey", newCache, cacheInvalidateMissingKey)
		suiteRun(t, "Clear", newCache, cacheClear)
	})
	if tagged {
		t.Run("Tagged", func(t *testing.T) {
			suiteRun(t, "InvalidateAnyTag", newCache, taggedInvalidateAnyTag)
		})
	}
	if stale {
		t.Run("Stale", func(t *testing.T) {
			suiteRun(t, "MarkKey", newCache, staleMarkKey)
			if tagged {
				suiteRun(t, "MarkTags", newCache, staleMarkTags)
			}
			suiteRun(t, "FreshWriteClears", newCache, staleFreshWriteClears)
		})
	}
}

// SharedCacheSuite checks that a store meets the collage.SharedCache contract.
// newPair returns two handles on one empty store, the way two instances of a
// fleet hold two connections to one Redis; a store that cannot tell handles
// apart may return the same value twice. It runs CacheSuite on the first handle,
// then a "Shared" section where each subtest writes through one handle and
// checks through the other: the epoch ordering that makes a write that raced an
// invalidation stale or skipped, what DropTags reaches, data values, and
// concurrent use (meaningful under -race). Each subtest calls newPair for a
// fresh pair.
func SharedCacheSuite(t *testing.T, newPair func(t *testing.T) (a, b collage.SharedCache)) {
	t.Helper()
	CacheSuite(t, func(t *testing.T) collage.Cache {
		a, _ := newPair(t)
		return a
	})
	t.Run("Shared", func(t *testing.T) {
		for _, c := range []struct {
			name string
			fn   func(t *testing.T, a, b collage.SharedCache)
		}{
			{"EpochGrows", sharedEpochGrows},
			{"OverlapStale", sharedOverlapStale},
			{"OverlapPurgeSkipped", sharedOverlapPurgeSkipped},
			{"UnrelatedTagFresh", sharedUnrelatedTagFresh},
			{"DropReachesOtherHandle", sharedDropReachesOtherHandle},
			{"PurgeRemoves", sharedPurgeRemoves},
			{"DataRoundTrip", sharedDataRoundTrip},
			{"DataExpiredIsStale", sharedDataExpiredIsStale},
			{"DataZeroTTLNeverExpires", sharedDataZeroTTLNeverExpires},
			{"DataInvalidateStale", sharedDataInvalidateStale},
			{"DataDelete", sharedDataDelete},
			{"DataOverlap", sharedDataOverlap},
			{"WideDrop", sharedWideDrop},
			{"Concurrent", sharedConcurrent},
		} {
			t.Run(c.name, func(t *testing.T) {
				a, b := newPair(t)
				c.fn(t, a, b)
			})
		}
	})
}

// suiteRun runs one subtest of CacheSuite against a fresh store, picking the
// store's capability interface the subtest needs from the Cache it was given.
func suiteRun[T collage.Cache](t *testing.T, name string, newCache func(t *testing.T) collage.Cache, fn func(t *testing.T, c T)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		store := newCache(t)
		c, ok := store.(T)
		if !ok {
			t.Fatalf("newCache returned %T, which no longer implements the interface the first store did", store)
		}
		fn(t, c)
	})
}

// eventually polls cond every suitePoll until it holds, failing with msg after
// suiteWait.
func eventually(t *testing.T, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(suiteWait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s (still false after %v)", msg, suiteWait)
		}
		time.Sleep(suitePoll)
	}
}

func must(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s returned error %v, want nil", what, err)
	}
}

func cacheRoundTrip(t *testing.T, c collage.Cache) {
	ctx := context.Background()
	body := []byte("hello page")
	etag, err := c.Set(ctx, "k", body, time.Minute)
	must(t, "Set", err)
	if etag == "" {
		t.Fatal("Set returned an empty etag, want the content's ETag")
	}
	got, gotEtag, found := c.Get(ctx, "k")
	if !found {
		t.Fatal("Get missed a key just Set with a minute TTL")
	}
	if !bytes.Equal(got, body) {
		t.Errorf("Get content = %q, want the bytes Set stored, %q", got, body)
	}
	if gotEtag != etag {
		t.Errorf("Get etag = %q, want the etag Set returned, %q", gotEtag, etag)
	}
	if _, _, found := c.Get(ctx, "other"); found {
		t.Error("Get found a key that was never Set")
	}
	otherEtag, err := c.Set(ctx, "k2", []byte("different page"), time.Minute)
	must(t, "Set", err)
	if otherEtag == etag {
		t.Errorf("Set returned etag %q for different content, want an etag that changes with the content", otherEtag)
	}
}

func cacheNotAliased(t *testing.T, c collage.Cache) {
	ctx := context.Background()
	body := []byte("original")
	_, err := c.Set(ctx, "k", body, time.Minute)
	must(t, "Set", err)
	copy(body, "MUTATED!")
	got, _, found := c.Get(ctx, "k")
	if !found {
		t.Fatal("Get missed a key just Set")
	}
	if string(got) != "original" {
		t.Errorf("Get content = %q after the caller mutated the slice it passed to Set, want %q: Set must not keep the caller's slice", got, "original")
	}
}

func cacheTTLExpires(t *testing.T, c collage.Cache) {
	ctx := context.Background()
	_, err := c.Set(ctx, "k", []byte("body"), suiteTTL)
	must(t, "Set", err)
	eventually(t, "Get still found an entry long past its TTL: an expired entry must not be reported by Get", func() bool {
		_, _, found := c.Get(ctx, "k")
		return !found
	})
}

func cacheInvalidateKey(t *testing.T, c collage.Cache) {
	ctx := context.Background()
	for _, k := range []string{"k1", "k2"} {
		_, err := c.Set(ctx, k, []byte(k), time.Minute)
		must(t, "Set", err)
	}
	must(t, "InvalidateKey", c.InvalidateKey(ctx, "k1"))
	if _, _, found := c.Get(ctx, "k1"); found {
		t.Error("Get found k1 after InvalidateKey(k1)")
	}
	if _, _, found := c.Get(ctx, "k2"); !found {
		t.Error("InvalidateKey(k1) removed k2: it must remove only its key")
	}
}

func cacheInvalidateMissingKey(t *testing.T, c collage.Cache) {
	must(t, "InvalidateKey of a key with no entry", c.InvalidateKey(context.Background(), "never-set"))
}

func cacheClear(t *testing.T, c collage.Cache) {
	ctx := context.Background()
	for _, k := range []string{"k1", "k2", "k3"} {
		_, err := c.Set(ctx, k, []byte(k), time.Minute)
		must(t, "Set", err)
	}
	must(t, "Clear", c.Clear(ctx))
	for _, k := range []string{"k1", "k2", "k3"} {
		if _, _, found := c.Get(ctx, k); found {
			t.Errorf("Get found %s after Clear: Clear must remove every entry", k)
		}
	}
}

func taggedInvalidateAnyTag(t *testing.T, c collage.TaggedCache) {
	ctx := context.Background()
	for k, tags := range map[string][]string{"k1": {"a", "b"}, "k2": {"b"}, "k3": {"c"}} {
		_, err := c.SetTagged(ctx, k, []byte(k), time.Minute, tags)
		must(t, "SetTagged", err)
	}
	must(t, "Invalidate", c.Invalidate(ctx, []string{"b"}))
	for _, k := range []string{"k1", "k2"} {
		if _, _, found := c.Get(ctx, k); found {
			t.Errorf("Get found %s after Invalidate([b]): Invalidate must remove every entry carrying any of the tags", k)
		}
	}
	if _, _, found := c.Get(ctx, "k3"); !found {
		t.Error("Invalidate([b]) removed k3, which does not carry tag b")
	}
}

// wantStale asserts that key is a miss for Get and a stale hit for GetStale with
// the given content and etag.
func wantStale(t *testing.T, c collage.StaleCache, key string, content []byte, etag string) {
	t.Helper()
	ctx := context.Background()
	if _, _, found := c.Get(ctx, key); found {
		t.Errorf("Get found %s, want a miss: a stale entry is not fresh", key)
	}
	got, gotEtag, stale, found := c.GetStale(ctx, key)
	if !found {
		t.Fatalf("GetStale missed %s, want the stale entry kept", key)
	}
	if !stale {
		t.Errorf("GetStale(%s) stale = false, want true", key)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("GetStale(%s) content = %q, want the content stored, %q", key, got, content)
	}
	if gotEtag != etag {
		t.Errorf("GetStale(%s) etag = %q, want the stored etag %q", key, gotEtag, etag)
	}
}

func staleMarkKey(t *testing.T, c collage.StaleCache) {
	ctx := context.Background()
	etag, err := c.Set(ctx, "k", []byte("body"), time.Minute)
	must(t, "Set", err)
	must(t, "MarkStaleKey", c.MarkStaleKey(ctx, "k"))
	wantStale(t, c, "k", []byte("body"), etag)
	must(t, "MarkStaleKey of a key with no entry", c.MarkStaleKey(ctx, "never-set"))
}

func staleMarkTags(t *testing.T, c collage.StaleCache) {
	ctx := context.Background()
	tc, ok := c.(collage.TaggedCache)
	if !ok {
		t.Skip("store is not a TaggedCache")
	}
	e1, err := tc.SetTagged(ctx, "k1", []byte("one"), time.Minute, []string{"a"})
	must(t, "SetTagged", err)
	_, err = tc.SetTagged(ctx, "k2", []byte("two"), time.Minute, []string{"b"})
	must(t, "SetTagged", err)
	must(t, "MarkStale", c.MarkStale(ctx, []string{"a"}))
	wantStale(t, c, "k1", []byte("one"), e1)
	if _, _, found := c.Get(ctx, "k2"); !found {
		t.Error("MarkStale([a]) made k2 stale, which does not carry tag a")
	}
}

func staleFreshWriteClears(t *testing.T, c collage.StaleCache) {
	ctx := context.Background()
	_, err := c.Set(ctx, "k", []byte("old"), time.Minute)
	must(t, "Set", err)
	must(t, "MarkStaleKey", c.MarkStaleKey(ctx, "k"))
	etag, err := c.Set(ctx, "k", []byte("new"), time.Minute)
	must(t, "Set", err)
	got, gotEtag, found := c.Get(ctx, "k")
	if !found {
		t.Fatal("Get missed after a fresh write to a stale key: a later write must clear the stale mark")
	}
	if string(got) != "new" || gotEtag != etag {
		t.Errorf("Get = %q %q, want the new content %q with etag %q", got, gotEtag, "new", etag)
	}
	if _, _, stale, _ := c.GetStale(ctx, "k"); stale {
		t.Error("GetStale reports stale after a fresh write")
	}
}

func sharedEpoch(t *testing.T, c collage.SharedCache) uint64 {
	t.Helper()
	e, err := c.Epoch(context.Background())
	must(t, "Epoch", err)
	return e
}

func drop(t *testing.T, c collage.SharedCache, tags []string, purge bool) []collage.Reached {
	t.Helper()
	r, err := c.DropTags(context.Background(), tags, purge)
	must(t, "DropTags", err)
	return r
}

func sharedEpochGrows(t *testing.T, a, b collage.SharedCache) {
	before := sharedEpoch(t, a)
	drop(t, b, []string{"t"}, false)
	if after := sharedEpoch(t, a); after <= before {
		t.Errorf("Epoch through the other handle = %d after DropTags, want more than %d: the epoch must grow with each DropTags and be shared", after, before)
	}
	again := sharedEpoch(t, b)
	drop(t, a, []string{"t"}, true)
	if after := sharedEpoch(t, b); after <= again {
		t.Errorf("Epoch = %d after a purging DropTags, want more than %d", after, again)
	}
}

func sharedOverlap(t *testing.T, a, b collage.SharedCache, purge bool, want collage.WriteOutcome) {
	ctx := context.Background()
	since := sharedEpoch(t, a)
	drop(t, b, []string{"t"}, purge)
	etag, outcome, err := a.SetSince(ctx, "k", []byte("body"), time.Minute, []string{"t"}, nil, since)
	must(t, "SetSince", err)
	if outcome != want {
		t.Errorf("SetSince outcome = %v, want %v: a tag dropped (purge=%v) after since decides the write", outcome, want, purge)
	}
	if want == collage.WriteSkipped {
		if _, _, _, found := b.GetStale(ctx, "k"); found {
			t.Error("a skipped write left an entry behind")
		}
	} else {
		wantStale(t, b, "k", []byte("body"), etag)
	}
	// The boundary is strict: a generation equal to since is not "after" it, so a
	// write that began once the drop had happened is fresh.
	since = sharedEpoch(t, a)
	_, outcome, err = a.SetSince(ctx, "k2", []byte("body"), time.Minute, []string{"t"}, nil, since)
	must(t, "SetSince", err)
	if outcome != collage.WriteFresh {
		t.Errorf("SetSince outcome = %v with since read after the drop (purge=%v), want WriteFresh: only a generation greater than since counts", outcome, purge)
	}
	if _, _, found := b.Get(ctx, "k2"); !found {
		t.Errorf("Get missed a write that began after the drop (purge=%v)", purge)
	}
}

func sharedOverlapStale(t *testing.T, a, b collage.SharedCache) {
	sharedOverlap(t, a, b, false, collage.WriteStale)
	// A write is stale when any one of its tags was invalidated, not all of them.
	ctx := context.Background()
	since := sharedEpoch(t, a)
	drop(t, b, []string{"t1"}, false)
	_, outcome, err := a.SetSince(ctx, "any", []byte("body"), time.Minute, []string{"t1", "z"}, nil, since)
	must(t, "SetSince", err)
	if outcome != collage.WriteStale {
		t.Errorf("SetSince outcome = %v for tags [t1 z] with only t1 invalidated, want WriteStale: any one tag decides", outcome)
	}
}

func sharedOverlapPurgeSkipped(t *testing.T, a, b collage.SharedCache) {
	sharedOverlap(t, a, b, true, collage.WriteSkipped)
	// Skipped outranks stale: a purge of one tag wins over an invalidation of
	// another, whatever order the tags are listed in.
	ctx := context.Background()
	since := sharedEpoch(t, a)
	drop(t, b, []string{"t1"}, false)
	drop(t, b, []string{"t2"}, true)
	for _, tags := range [][]string{{"t1", "t2"}, {"t2", "t1"}} {
		_, outcome, err := a.SetSince(ctx, "both", []byte("body"), time.Minute, tags, nil, since)
		must(t, "SetSince", err)
		if outcome != collage.WriteSkipped {
			t.Errorf("SetSince outcome = %v for tags %v with t1 invalidated and t2 purged, want WriteSkipped", outcome, tags)
		}
	}
}

func sharedUnrelatedTagFresh(t *testing.T, a, b collage.SharedCache) {
	ctx := context.Background()
	since := sharedEpoch(t, a)
	drop(t, b, []string{"x"}, true)
	_, outcome, err := a.SetSince(ctx, "k", []byte("body"), time.Minute, []string{"y"}, nil, since)
	must(t, "SetSince", err)
	if outcome != collage.WriteFresh {
		t.Errorf("SetSince outcome = %v, want WriteFresh: a drop of tag x must not affect a write carrying only tag y", outcome)
	}
	if _, _, found := b.Get(ctx, "k"); !found {
		t.Error("Get through the other handle missed a fresh write")
	}
}

func sharedDropReachesOtherHandle(t *testing.T, a, b collage.SharedCache) {
	ctx := context.Background()
	meta := []byte("route=/x")
	etag, outcome, err := a.SetSince(ctx, "k", []byte("body"), time.Minute, []string{"t"}, meta, sharedEpoch(t, a))
	must(t, "SetSince", err)
	if outcome != collage.WriteFresh {
		t.Fatalf("SetSince outcome = %v, want WriteFresh", outcome)
	}
	if _, _, found := b.Get(ctx, "k"); !found {
		t.Fatal("Get through the other handle missed a page written through this one")
	}
	// A page under another tag and a data value under this one must survive: the
	// drop reaches pages under its tags only, and never lists data entries.
	_, _, err = a.SetSince(ctx, "other", []byte("other body"), time.Minute, []string{"o"}, nil, sharedEpoch(t, a))
	must(t, "SetSince", err)
	setData(t, a, "d", "value", time.Minute, []string{"t"}, sharedEpoch(t, a))
	reached := drop(t, b, []string{"t"}, false)
	if len(reached) != 1 || reached[0].Key != "k" || !bytes.Equal(reached[0].Meta, meta) {
		t.Fatalf("DropTags reached %+v, want exactly key k with meta %q: not pages under other tags, not data entries", reached, meta)
	}
	if _, _, found := b.Get(ctx, "other"); !found {
		t.Error("DropTags([t]) made a page under tag o stale: it must touch only entries under its tags")
	}
	if _, _, found := a.Get(ctx, "k"); found {
		t.Error("Get through the writing handle found the page after the other handle dropped its tag")
	}
	got, gotEtag, stale, found := a.GetStale(ctx, "k")
	if !found || !stale || string(got) != "body" || gotEtag != etag {
		t.Errorf("GetStale = %q %q stale=%v found=%v, want the page kept stale with its etag %q", got, gotEtag, stale, found, etag)
	}
}

func sharedPurgeRemoves(t *testing.T, a, b collage.SharedCache) {
	ctx := context.Background()
	_, _, err := a.SetSince(ctx, "k", []byte("body"), time.Minute, []string{"t"}, nil, sharedEpoch(t, a))
	must(t, "SetSince", err)
	_, err = a.SetData(ctx, "d", []byte("val"), 0, []string{"t"}, sharedEpoch(t, a))
	must(t, "SetData", err)
	drop(t, b, []string{"t"}, true)
	if _, _, _, found := a.GetStale(ctx, "k"); found {
		t.Error("GetStale found a page after a purging DropTags, want it removed")
	}
	_, _, found, err := a.GetData(ctx, "d")
	must(t, "GetData", err)
	if found {
		t.Error("GetData found a value under a purged tag, want found == false")
	}
}

func setData(t *testing.T, c collage.SharedCache, key, value string, ttl time.Duration, tags []string, since uint64) collage.WriteOutcome {
	t.Helper()
	o, err := c.SetData(context.Background(), key, []byte(value), ttl, tags, since)
	must(t, "SetData", err)
	return o
}

func getData(t *testing.T, c collage.SharedCache, key string) (value string, stale, found bool) {
	t.Helper()
	v, stale, found, err := c.GetData(context.Background(), key)
	must(t, "GetData", err)
	return string(v), stale, found
}

func sharedDataRoundTrip(t *testing.T, a, b collage.SharedCache) {
	if o := setData(t, a, "d", "value", time.Minute, []string{"t"}, sharedEpoch(t, a)); o != collage.WriteFresh {
		t.Fatalf("SetData outcome = %v, want WriteFresh", o)
	}
	v, stale, found := getData(t, b, "d")
	if !found || stale || v != "value" {
		t.Errorf("GetData through the other handle = %q stale=%v found=%v, want %q fresh", v, stale, found, "value")
	}
	if _, _, found := getData(t, b, "missing"); found {
		t.Error("GetData found a key that was never set")
	}
}

func sharedDataExpiredIsStale(t *testing.T, a, b collage.SharedCache) {
	setData(t, a, "d", "value", suiteTTL, nil, sharedEpoch(t, a))
	eventually(t, "GetData never reported the expired value stale", func() bool {
		_, stale, _ := getData(t, b, "d")
		return stale
	})
	v, stale, found := getData(t, b, "d")
	if !found || !stale || v != "value" {
		t.Errorf("GetData after expiry = %q stale=%v found=%v, want the value kept, found and stale", v, stale, found)
	}
}

func sharedDataZeroTTLNeverExpires(t *testing.T, a, b collage.SharedCache) {
	setData(t, a, "d", "value", 0, nil, sharedEpoch(t, a))
	time.Sleep(200 * time.Millisecond)
	v, stale, found := getData(t, b, "d")
	if !found || stale || v != "value" {
		t.Errorf("GetData after 200ms = %q stale=%v found=%v, want fresh: a data ttl of zero never expires", v, stale, found)
	}
}

func sharedDataInvalidateStale(t *testing.T, a, b collage.SharedCache) {
	setData(t, a, "d", "value", time.Minute, []string{"t"}, sharedEpoch(t, a))
	drop(t, b, []string{"t"}, false)
	v, stale, found := getData(t, a, "d")
	if !found || !stale || v != "value" {
		t.Errorf("GetData after an invalidating DropTags = %q stale=%v found=%v, want the value kept, found and stale", v, stale, found)
	}
}

func sharedDataDelete(t *testing.T, a, b collage.SharedCache) {
	ctx := context.Background()
	setData(t, a, "d", "value", time.Minute, nil, sharedEpoch(t, a))
	must(t, "DeleteData", b.DeleteData(ctx, "d"))
	if _, _, found := getData(t, a, "d"); found {
		t.Error("GetData found a value after DeleteData through the other handle")
	}
	must(t, "DeleteData of a key with no value", b.DeleteData(ctx, "never-set"))
}

func sharedDataOverlap(t *testing.T, a, b collage.SharedCache) {
	since := sharedEpoch(t, a)
	drop(t, b, []string{"t"}, false)
	if o := setData(t, a, "stale", "v", time.Minute, []string{"t"}, since); o != collage.WriteStale {
		t.Errorf("SetData outcome = %v after an invalidation past since, want WriteStale", o)
	}
	if v, stale, found := getData(t, b, "stale"); !found || !stale || v != "v" {
		t.Errorf("GetData of a value written stale = %q found=%v stale=%v, want the value kept, found and stale", v, found, stale)
	}
	drop(t, b, []string{"u"}, true)
	if o := setData(t, a, "skipped", "v", time.Minute, []string{"u"}, since); o != collage.WriteSkipped {
		t.Errorf("SetData outcome = %v after a purge past since, want WriteSkipped", o)
	}
	if _, _, found := getData(t, b, "skipped"); found {
		t.Error("GetData found a value whose write was skipped")
	}
	if o := setData(t, a, "unrelated", "v", time.Minute, []string{"w"}, since); o != collage.WriteFresh {
		t.Errorf("SetData outcome = %v with a tag nothing dropped, want WriteFresh", o)
	}
	// The boundary is strict: with since read after the drops, a generation equal
	// to it is not "after", so the writes are fresh.
	since = sharedEpoch(t, a)
	for _, tag := range []string{"t", "u"} {
		if o := setData(t, a, "after-"+tag, "v", time.Minute, []string{tag}, since); o != collage.WriteFresh {
			t.Errorf("SetData outcome = %v for tag %s with since read after its drop, want WriteFresh: only a generation greater than since counts", o, tag)
		}
	}
}

func sharedWideDrop(t *testing.T, a, b collage.SharedCache) {
	const n = 5000
	ctx := context.Background()
	since := sharedEpoch(t, a)
	for i := range n {
		_, o, err := a.SetSince(ctx, fmt.Sprintf("page-%d", i), []byte("body"), time.Minute, []string{"wide"}, nil, since)
		must(t, "SetSince", err)
		if o != collage.WriteFresh {
			t.Fatalf("SetSince #%d outcome = %v, want WriteFresh", i, o)
		}
	}
	reached := drop(t, b, []string{"wide"}, false)
	keys := make([]string, len(reached))
	for i, r := range reached {
		keys[i] = r.Key
	}
	slices.Sort(keys)
	if got := len(slices.Compact(keys)); got != n || len(reached) != n {
		t.Errorf("DropTags reached %d entries (%d distinct), want %d distinct: it must reach every entry under a tag, however many", len(reached), got, n)
	}
}

func sharedConcurrent(t *testing.T, a, b collage.SharedCache) {
	const workers, ops = 8, 200
	ctx := context.Background()
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			x, y := a, b
			if w%2 == 1 {
				x, y = b, a
			}
			for i := range ops {
				key := fmt.Sprintf("k-%d", i%10)
				tags := []string{fmt.Sprintf("t-%d", i%3)}
				since, err := x.Epoch(ctx)
				if err != nil {
					t.Errorf("Epoch returned error %v, want nil", err)
					return
				}
				switch i % 4 {
				case 0:
					_, _, err = x.SetSince(ctx, key, []byte("body"), time.Minute, tags, []byte("m"), since)
				case 1:
					y.Get(ctx, key)
				case 2:
					_, err = y.DropTags(ctx, tags, i%8 == 2)
				case 3:
					_, _, _, err = y.GetData(ctx, key)
					if err == nil {
						_, err = x.SetData(ctx, key, []byte("v"), 0, tags, since)
					}
				}
				if err != nil {
					t.Errorf("concurrent op %d returned error %v, want nil", i%4, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
