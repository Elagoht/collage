package types

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"time"
)

// CacheValue is one value the data cache holds: a caller's typed value that can
// write itself out, so the store can keep it on disk without knowing its type.
type CacheValue interface {
	Encode() ([]byte, error)
}

// DataCache is what Cached stores values in across renders. The framework
// supplies the store; the interface is declared beside RenderContext so a
// render can carry one without depending on how it is kept.
//
// decode turns what Encode wrote back into a value of the same type; a store
// that keeps values only in memory never calls it. stale reports a value the
// store kept after an invalidation or past its ttl, served because no fresher
// one is to be had yet.
type DataCache interface {
	Load(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (CacheValue, error), decode func([]byte) (CacheValue, error)) (value CacheValue, stale bool, err error)
}

// errInterfaceValue is why a value stored under an interface-typed key is kept in
// memory only: JSON records no concrete type to decode it back into.
var errInterfaceValue = errors.New("collage: a value of an interface type cannot be persisted")

// cachedValue is a T as the data cache holds it.
type cachedValue[T any] struct{ value T }

// Encode refuses a T that JSON would not bring back as it was: the store then
// keeps the value in memory only, where it is exact, rather than restore it
// after a restart with fields silently zeroed.
func (c cachedValue[T]) Encode() ([]byte, error) {
	if err := persistable(reflect.TypeFor[T]()); err != nil {
		return nil, err
	}
	return json.Marshal(c.value)
}

func decodeCached[T any](data []byte) (CacheValue, error) {
	if err := persistable(reflect.TypeFor[T]()); err != nil {
		return nil, err
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return cachedValue[T]{value: value}, nil
}

// BindDataCache gives rc — and every copy of it the render makes — the store behind
// Cached. The render engine calls it.
func BindDataCache(rc *RenderContext, store DataCache) {
	if rc != nil && rc.state != nil {
		rc.state.mu.Lock()
		rc.state.data = store
		rc.state.mu.Unlock()
	}
}

// SkipDataCache makes Cached fetch fresh for this render: neither serving a stored
// value nor storing the one it fetched. It is how a preview sees a draft.
func SkipDataCache(rc *RenderContext) {
	if rc != nil && rc.state != nil {
		rc.state.mu.Lock()
		rc.state.uncached = true
		rc.state.mu.Unlock()
	}
}

// DeclaredTags returns the dependency tags this render's Cached calls declared.
func DeclaredTags(rc *RenderContext) []string {
	if rc == nil || rc.state == nil {
		return nil
	}
	rc.state.mu.Lock()
	defer rc.state.mu.Unlock()
	return append([]string(nil), rc.state.tags...)
}

// Cached returns the value stored under key, fetching it when there is none. Unlike
// Once, what it stores outlives the render: every page that asks for one author's
// key shares one fetch until ttl passes or one of tags is invalidated.
//
// tags are also added to the render's own dependency tags, so a page built from
// the value is invalidated with it — one InvalidateTags("author:A") drops the
// author and every page showing them.
//
// Where there is no store to keep it in — development, a preview, a render the
// application built by hand, or a cache that is not enabled — it is Once: shared
// within the render, fetched fresh by the next.
func Cached[T any](rc *RenderContext, key Key[T], ttl time.Duration, tags []string, fetch func(context.Context) (T, error)) (T, error) {
	var zero T
	id := key.id()
	if rc == nil || rc.state == nil {
		if fetch == nil {
			return zero, nil
		}
		return fetch(context.Background())
	}

	rc.state.mu.Lock()
	rc.state.tags = append(rc.state.tags, tags...)
	store, uncached := rc.state.data, rc.state.uncached
	rc.state.mu.Unlock()

	if store == nil || uncached {
		// Namespaced, so a Cached key and a Once key the application happens to
		// spell alike do not share a fetch.
		return Once(rc, Key[T]{name: "collage:cached:" + key.name}, fetch)
	}

	value, stale, err := store.Load(rc.Context(), storeKey(id), ttl, tags, func(ctx context.Context) (CacheValue, error) {
		fetched, err := fetch(ctx)
		if err != nil {
			return nil, err
		}
		return cachedValue[T]{value: fetched}, nil
	}, decodeCached[T])
	if err != nil {
		return zero, err
	}
	if stale {
		rc.state.mu.Lock()
		rc.state.stale = true
		rc.state.mu.Unlock()
	}
	typed, ok := value.(cachedValue[T])
	if !ok {
		// The store key carries T, so only a store handing back what it was not
		// given gets here. Saying so beats an empty section.
		return zero, fmt.Errorf("collage: Cached %q: the store returned a %T, want %s", key.name, value, id.typ)
	}
	return typed.value, nil
}

// storeKey is the string the store keeps a value under: the key's name and its
// type, so two keys of one name never share an entry. It is the same in every
// process, because a store on disk outlives the process that wrote it.
func storeKey(id keyID) string {
	return id.name + "\x00" + typeIdentity(id.typ)
}

// typeMemo remembers one answer per type. Every Cached call asks, so the
// common case — a type asked about before — takes only the read lock.
type typeMemo[V any] struct {
	mu     sync.RWMutex
	byType map[reflect.Type]V
}

func (m *typeMemo[V]) get(t reflect.Type, compute func(reflect.Type) V) V {
	m.mu.RLock()
	v, ok := m.byType[t]
	m.mu.RUnlock()
	if ok {
		return v
	}
	// Computed outside the lock: the answer depends on t alone, so two callers
	// racing compute the same one and either may keep it.
	v = compute(t)
	m.mu.Lock()
	if m.byType == nil {
		m.byType = make(map[reflect.Type]V)
	}
	m.byType[t] = v
	m.mu.Unlock()
	return v
}

var identities typeMemo[string]

// typeIdentity names t the same way in every process that has it, and
// differently once its shape changes. encoding/json decodes a struct that gained
// or lost a field without complaint, so the name alone would hand the new build
// the old build's data, zeros where the new fields are.
func typeIdentity(t reflect.Type) string {
	return identities.get(t, func(t reflect.Type) string {
		sum := sha256.New()
		writeShape(sum, t, make(map[reflect.Type]bool))
		return t.PkgPath() + "\x00" + t.String() + "\x00" + hex.EncodeToString(sum.Sum(nil)[:8])
	})
}

var persistability typeMemo[error]

// persistable reports why a value of type t would not come back from JSON as
// it went in, or nil when it would. JSON writes no unexported field and no
// field tagged json:"-", so they come back zero; and it records no concrete
// type under an interface, so what comes back is maps and float64s. A type
// that writes and reads itself — time.Time, say, all unexported fields — is
// trusted to round-trip, and not looked into.
func persistable(t reflect.Type) error {
	return persistability.get(t, func(t reflect.Type) error {
		if t.Kind() == reflect.Interface {
			return errInterfaceValue
		}
		if problem := persistProblem(t, make(map[reflect.Type]bool)); problem != "" {
			return fmt.Errorf("collage: a %s cannot be persisted: %s", t, problem)
		}
		return nil
	})
}

var (
	jsonMarshaler   = reflect.TypeFor[json.Marshaler]()
	jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()
)

// persistProblem walks t as writeShape does and describes the first thing in
// it JSON would not bring back, or returns "" when there is none. seen stops
// a type that refers to itself.
func persistProblem(t reflect.Type, seen map[reflect.Type]bool) string {
	if codesItself(t) || seen[t] {
		return ""
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Interface:
		return fmt.Sprintf("it holds the interface %s", t)
	case reflect.Struct:
		for i := range t.NumField() {
			field := t.Field(i)
			if !field.IsExported() {
				return fmt.Sprintf("%s.%s is unexported", t, field.Name)
			}
			if field.Tag.Get("json") == "-" {
				return fmt.Sprintf("%s.%s is tagged json:\"-\"", t, field.Name)
			}
			if problem := persistProblem(field.Type, seen); problem != "" {
				return problem
			}
		}
	case reflect.Map:
		if problem := persistProblem(t.Key(), seen); problem != "" {
			return problem
		}
		return persistProblem(t.Elem(), seen)
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return persistProblem(t.Elem(), seen)
	}
	return ""
}

// codesItself reports whether t, or a pointer to it, both writes and reads
// itself as JSON.
func codesItself(t reflect.Type) bool {
	implements := func(iface reflect.Type) bool {
		return t.Implements(iface) || (t.Kind() != reflect.Pointer && reflect.PointerTo(t).Implements(iface))
	}
	return implements(jsonMarshaler) && implements(jsonUnmarshaler)
}

// writeShape writes what decides how t encodes: its kind and name, and, through
// structs, pointers, slices, arrays and maps, the same of what it holds. seen
// stops a type that refers to itself.
func writeShape(w io.Writer, t reflect.Type, seen map[reflect.Type]bool) {
	fmt.Fprintf(w, "%s|%s|%s;", t.Kind(), t.PkgPath(), t.String())
	if seen[t] {
		return
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Struct:
		for i := range t.NumField() {
			field := t.Field(i)
			fmt.Fprintf(w, "{%s %q}", field.Name, field.Tag.Get("json"))
			writeShape(w, field.Type, seen)
		}
	case reflect.Map:
		writeShape(w, t.Key(), seen)
		writeShape(w, t.Elem(), seen)
	case reflect.Pointer, reflect.Slice, reflect.Array:
		writeShape(w, t.Elem(), seen)
	}
}

// ServedStale reports whether any Cached call in this render was served a
// stale value.
func ServedStale(rc *RenderContext) bool {
	if rc == nil || rc.state == nil {
		return false
	}
	rc.state.mu.Lock()
	defer rc.state.mu.Unlock()
	return rc.state.stale
}
