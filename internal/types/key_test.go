package types

import (
	"context"
	"sync"
	"testing"
)

type article struct{ Title string }

func TestKey_GetSet(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	k := NewKey[article]("article")
	if _, ok := k.Get(rc); ok {
		t.Fatal("Get before Set = true")
	}
	k.Set(rc, article{Title: "a"})
	if got, ok := k.Get(rc); !ok || got.Title != "a" {
		t.Errorf("Get = %+v, %v", got, ok)
	}
}

func TestKey_IdentityIsNameAndType(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	NewKey[string]("x").Set(rc, "s")
	NewKey[int]("x").Set(rc, 7)
	if s, _ := NewKey[string]("x").Get(rc); s != "s" {
		t.Errorf("string key = %q", s)
	}
	if n, _ := NewKey[int]("x").Get(rc); n != 7 {
		t.Errorf("int key = %d", n)
	}
	// A key made again, elsewhere, with the same name and type is the same key.
	if s, ok := NewKey[string]("x").Get(rc); !ok || s != "s" {
		t.Errorf("recreated key = %q, %v", s, ok)
	}
}

func TestKey_With(t *testing.T) {
	k := NewKey[article]("article").With("a").With("b")
	if k.Name() != "article:a:b" {
		t.Errorf("Name = %q", k.Name())
	}
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	k.Set(rc, article{Title: "ab"})
	if _, ok := NewKey[article]("article").With("a").Get(rc); ok {
		t.Error("article:a found the value of article:a:b")
	}
}

func TestKey_EmptyNamePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewKey(\"\") did not panic")
		}
	}()
	NewKey[int]("")
}

func TestKey_SharedAcrossCopiesAndConcurrent(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	k := NewKey[int]("n")
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			child := rc.WithFragment(1, i).WithContext(context.Background())
			NewKey[int]("n").With(string(rune('a'+i%26))).Set(child, i)
			k.Get(child)
		}()
	}
	wg.Wait()
	k.Set(rc.WithFragment(1, 0), 1)
	if n, ok := k.Get(rc); !ok || n != 1 {
		t.Errorf("a copy's Set is not visible to the original: %d, %v", n, ok)
	}
}

func TestKey_HandBuiltContext(t *testing.T) {
	rc := &RenderContext{}
	k := NewKey[int]("n")
	if _, ok := k.Get(rc); ok {
		t.Fatal("Get on a hand-built context = true")
	}
	k.Set(rc, 3)
	if n, ok := k.Get(rc); !ok || n != 3 {
		t.Errorf("Get after Set = %d, %v", n, ok)
	}
}

func TestKey_In(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	k := NewKey[article]("article")
	k.Set(rc, article{Title: "a"})
	if got, ok := k.In(ValuesOf(rc)); !ok || got.Title != "a" {
		t.Errorf("In = %+v, %v", got, ok)
	}
	if _, ok := k.In(nil); ok {
		t.Error("In(nil) = true")
	}
	if _, ok := NewKey[article]("other").In(ValuesOf(rc)); ok {
		t.Error("In with a key never set = true")
	}
}
