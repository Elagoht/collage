package types

import (
	"context"
	"fmt"
	"testing"
)

// A key is its name and its type: one name asked for as two types is two values.
func TestOnce_SameNameDifferentTypes(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	s, err := Once(rc, NewKey[string]("x"), func(context.Context) (string, error) { return "s", nil })
	if err != nil || s != "s" {
		t.Fatalf("string Once = %q, %v", s, err)
	}
	n, err := Once(rc, NewKey[int]("x"), func(context.Context) (int, error) { return 7, nil })
	if err != nil || n != 7 {
		t.Errorf("int Once = %d, %v: one name, two types, two values", n, err)
	}
}

// A nil fetched under an interface-typed key is a result like any other: the
// caller that fetched it and the one that finds it both get nil and no error.
func TestOnce_NilUnderAnInterfaceKey(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	key := NewKey[fmt.Stringer]("s")
	calls := 0
	fetch := func(context.Context) (fmt.Stringer, error) { calls++; return nil, nil }
	for i := range 2 {
		v, err := Once(rc, key, fetch)
		if err != nil || v != nil {
			t.Errorf("call %d = %v, %v; want nil, nil", i, v, err)
		}
	}
	if calls != 1 {
		t.Errorf("fetched %d times, want once", calls)
	}
}
