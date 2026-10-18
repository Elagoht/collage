package cache

import (
	"context"
	"reflect"
	"testing"
	"time"
)

var _ SharedCache = (*SharedMemory)(nil)

func TestSharedMemory_SetSinceAfterInvalidateIsStale(t *testing.T) {
	ctx := context.Background()
	c := NewSharedMemory()
	e, err := c.Epoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DropTags(ctx, []string{"t"}, false); err != nil {
		t.Fatal(err)
	}
	_, outcome, err := c.SetSince(ctx, "k", []byte("body"), time.Minute, []string{"t"}, nil, e)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != WriteStale {
		t.Fatalf("outcome = %v, want WriteStale", outcome)
	}
	if _, _, found := c.Get(ctx, "k"); found {
		t.Error("Get found a stale entry")
	}
	content, _, stale, found := c.GetStale(ctx, "k")
	if !found || !stale || string(content) != "body" {
		t.Errorf("GetStale = %q stale=%v found=%v, want body stale found", content, stale, found)
	}
}

func TestSharedMemory_SetSinceAfterPurgeIsSkipped(t *testing.T) {
	ctx := context.Background()
	c := NewSharedMemory()
	e, _ := c.Epoch(ctx)
	if _, err := c.DropTags(ctx, []string{"t"}, true); err != nil {
		t.Fatal(err)
	}
	_, outcome, err := c.SetSince(ctx, "k", []byte("body"), time.Minute, []string{"t"}, nil, e)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != WriteSkipped {
		t.Fatalf("outcome = %v, want WriteSkipped", outcome)
	}
	if _, _, _, found := c.GetStale(ctx, "k"); found {
		t.Error("a skipped write left an entry")
	}
}

func TestSharedMemory_DropTagsReturnsMeta(t *testing.T) {
	ctx := context.Background()
	c := NewSharedMemory()
	e, _ := c.Epoch(ctx)
	if _, _, err := c.SetSince(ctx, "k", []byte("p"), 0, []string{"t"}, []byte("m"), e); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetData(ctx, "d", []byte("v"), 0, []string{"t"}, e); err != nil {
		t.Fatal(err)
	}
	got, err := c.DropTags(ctx, []string{"t"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Reached{{Key: "k", Meta: []byte("m")}}; !reflect.DeepEqual(got, want) {
		t.Errorf("DropTags = %v, want %v", got, want)
	}
	if _, stale, found, _ := c.GetData(ctx, "d"); !found || !stale {
		t.Errorf("data after DropTags: stale=%v found=%v, want stale found", stale, found)
	}
	if _, _, _, found := c.GetStale(ctx, "k"); !found {
		t.Error("an invalidated page was removed, want kept stale")
	}
}

func TestSharedMemory_EpochOnlyGrowsAndFreshWriteClearsStale(t *testing.T) {
	ctx := context.Background()
	c := NewSharedMemory()
	e1, _ := c.Epoch(ctx)
	if e1 == 0 {
		t.Fatal("epoch starts at 0, want 1")
	}
	if _, _, err := c.SetSince(ctx, "k", []byte("a"), 0, []string{"t"}, nil, e1); err != nil {
		t.Fatal(err)
	}
	c.DropTags(ctx, []string{"t"}, false)
	e2, _ := c.Epoch(ctx)
	if e2 != e1+1 {
		t.Errorf("epoch = %d, want %d", e2, e1+1)
	}
	if _, outcome, _ := c.SetSince(ctx, "k", []byte("b"), 0, []string{"t"}, nil, e2); outcome != WriteFresh {
		t.Errorf("outcome = %v, want WriteFresh", outcome)
	}
	if got, _, found := c.Get(ctx, "k"); !found || string(got) != "b" {
		t.Errorf("Get = %q found=%v, want b", got, found)
	}
}

func TestSharedMemory_DataExpiryAndDelete(t *testing.T) {
	ctx := context.Background()
	c := NewSharedMemory()
	if _, err := c.SetData(ctx, "d", []byte("v"), time.Millisecond, nil, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if v, stale, found, _ := c.GetData(ctx, "d"); !found || !stale || string(v) != "v" {
		t.Errorf("expired data: %q stale=%v found=%v", v, stale, found)
	}
	if err := c.DeleteData(ctx, "d"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteData(ctx, "d"); err != nil {
		t.Errorf("deleting a missing key: %v", err)
	}
	if _, _, found, _ := c.GetData(ctx, "d"); found || c.Data() != 0 {
		t.Error("deleted data still found")
	}
}

func TestSharedMemory_MarkAndInvalidateAndCopies(t *testing.T) {
	ctx := context.Background()
	c := NewSharedMemory()
	body := []byte("abc")
	etag, err := c.SetTagged(ctx, "k", body, 0, []string{"t"})
	if err != nil || etag != ETag([]byte("abc")) {
		t.Fatalf("etag = %q err=%v", etag, err)
	}
	body[0] = 'X'
	got, _, _ := c.Get(ctx, "k")
	if string(got) != "abc" {
		t.Errorf("stored content aliased the caller's slice: %q", got)
	}
	c.MarkStaleKey(ctx, "k")
	if _, _, stale, _ := c.GetStale(ctx, "k"); !stale {
		t.Error("MarkStaleKey did not mark")
	}
	if _, e2, _, _ := c.GetStale(ctx, "k"); e2 != etag {
		t.Error("marking changed the ETag")
	}
	c.Invalidate(ctx, []string{"t"})
	if _, _, _, found := c.GetStale(ctx, "k"); found || c.Pages() != 0 {
		t.Error("Invalidate left the entry")
	}
}
