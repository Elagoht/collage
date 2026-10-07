package types

import (
	"reflect"
	"sync"
)

// Key names a value one render's fragments share, and fixes its type: the value a
// fragment stores with Set is the one another reads with Get, as the type the key
// says, and nothing else can be stored under it.
//
// A key is its name and its type. NewKey[A]("x") and NewKey[B]("x") are two
// keys; two keys equal in both are one, wherever and however often each was
// made. Declare keys as package variables and derive per-value ones with With.
//
// Always make a key with NewKey. A zero Key — a struct field nothing set — has
// no name, and using it panics rather than share one nameless value with every
// other zero key of its type.
type Key[T any] struct {
	name string
}

// NewKey returns the key named name for values of type T. An empty name is a
// programming error and panics where the key is declared.
func NewKey[T any](name string) Key[T] {
	if name == "" {
		panic("collage: NewKey with an empty name")
	}
	return Key[T]{name: name}
}

// With returns the key named k's name, a colon and part, for the same type:
// articleKey.With(slug) for one article among many.
func (k Key[T]) With(part string) Key[T] {
	return Key[T]{name: k.id().name + ":" + part}
}

// Name returns the key's name, for logs and errors.
func (k Key[T]) Name() string { return k.name }

// keyID is what a value is stored under: the key's name and its type.
type keyID struct {
	name string
	typ  reflect.Type
}

func (k Key[T]) id() keyID {
	if k.name == "" {
		panic("collage: a Key used without NewKey")
	}
	return keyID{name: k.name, typ: reflect.TypeFor[T]()}
}

// Get returns the value stored under k in rc's render, and whether one was.
func (k Key[T]) Get(rc *RenderContext) (T, bool) {
	return k.In(rc.bag(false))
}

// Set stores v under k in rc's render. Sibling fragments' data handlers run at
// the same time; Set and Get are safe between them. A Get that misses, followed
// by work and a Set, is two fragments doing that work twice — Once is the form
// without that gap.
func (k Key[T]) Set(rc *RenderContext, v T) {
	id := k.id()
	bag := rc.bag(true)
	bag.mu.Lock()
	defer bag.mu.Unlock()
	if bag.m == nil {
		bag.m = make(map[keyID]any) // any: the bag holds every key's type; Key restores it
	}
	bag.m[id] = v
}

// In returns the value stored under k in values — a finished render's, as an
// AfterRender hook receives them — and whether one was. A nil values has none.
func (k Key[T]) In(values *Values) (T, bool) {
	var zero T
	id := k.id()
	if values == nil {
		return zero, false
	}
	values.mu.Lock()
	defer values.mu.Unlock()
	v, ok := values.m[id]
	if !ok {
		return zero, false
	}
	if v == nil {
		// A nil stored under an interface-typed key: present, and nil. Asserting
		// it would fail and read as absent.
		return zero, true
	}
	typed, ok := v.(T)
	return typed, ok
}

// Values are what one render's fragments stored with Key.Set. They are opaque:
// a Key reads them with In.
type Values struct {
	mu sync.Mutex
	m  map[keyID]any // any: the bag holds every key's type; Key restores it
}

// ValuesOf returns rc's render's values, for collage to hand an AfterRender hook.
func ValuesOf(rc *RenderContext) *Values {
	return rc.bag(false)
}

// bag returns rc's values, allocating them on a hand-built context when create
// is set. A context from NewRenderContext always has them, so every copy the
// render makes shares one. A hand-built context is for single-goroutine use: the
// allocation is unguarded, and a copy made before its first Set does not share
// the values that Set creates.
func (rc *RenderContext) bag(create bool) *Values {
	if rc == nil {
		return nil
	}
	if rc.values == nil && create {
		rc.values = &Values{}
	}
	return rc.values
}
