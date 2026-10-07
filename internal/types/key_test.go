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

// A zero Key never went through NewKey, so it has no name; every use of it
// panics instead of sharing one nameless value with every other zero key.
func TestKey_ZeroKeyPanics(t *testing.T) {
	var zero Key[int]
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	uses := map[string]func(){
		"Set":  func() { zero.Set(rc, 1) },
		"Get":  func() { zero.Get(rc) },
		"In":   func() { zero.In(nil) },
		"With": func() { zero.With("x") },
		"Once": func() {
			_, _ = Once(rc, zero, func(context.Context) (int, error) { return 1, nil })
		},
		"Cached": func() {
			_, _ = Cached(rc, zero, 0, nil, func(context.Context) (int, error) { return 1, nil })
		},
	}
	for name, use := range uses {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != "collage: a Key used without NewKey" {
					t.Errorf("recovered %v, want the NewKey panic", r)
				}
			}()
			use()
		})
	}
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

func TestKey_NilInterfaceValue(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	errKey := NewKey[error]("err")
	errKey.Set(rc, nil)
	if err, ok := errKey.Get(rc); !ok || err != nil {
		t.Errorf("Key[error] after Set(nil) = %v, %v; want nil, true", err, ok)
	}
	anyKey := NewKey[any]("v") // any: a nil interface value is what this case stores
	anyKey.Set(rc, nil)
	if v, ok := anyKey.Get(rc); !ok || v != nil {
		t.Errorf("Key[any] after Set(nil) = %v, %v; want nil, true", v, ok)
	}
}
