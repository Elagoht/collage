package types

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// mapCache is a DataCache that keeps everything forever.
type mapCache map[string]CacheValue

func (m mapCache) Load(ctx context.Context, key string, _ time.Duration, _ []string, fetch func(context.Context) (CacheValue, error), _ func([]byte) (CacheValue, error)) (CacheValue, bool, error) {
	if v, ok := m[key]; ok {
		return v, false, nil
	}
	v, err := fetch(ctx)
	if err == nil {
		m[key] = v
	}
	return v, false, err
}

// A key is its name and its type: one name asked for as two types is two
// values, in the store and in the Once it falls back to. Both calls' tags are
// the render's.
func TestCached_SameNameDifferentTypes(t *testing.T) {
	for _, skip := range []bool{false, true} {
		rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
		store := mapCache{}
		BindDataCache(rc, store)
		if skip {
			SkipDataCache(rc)
		}
		s, err := Cached(rc, NewKey[string]("x"), time.Hour, []string{"t1"}, func(context.Context) (string, error) { return "s", nil })
		n, err2 := Cached(rc, NewKey[int]("x"), time.Hour, []string{"t2"}, func(context.Context) (int, error) { return 7, nil })
		if err != nil || err2 != nil || s != "s" || n != 7 {
			t.Errorf("skip=%v: %q %v / %d %v", skip, s, err, n, err2)
		}
		if tags := DeclaredTags(rc); !slices.Equal(tags, []string{"t1", "t2"}) {
			t.Errorf("skip=%v: DeclaredTags = %v, want both calls' tags", skip, tags)
		}
		if want := 2; !skip && len(store) != want {
			t.Errorf("store holds %d entries, want %d", len(store), want)
		}
	}
}

// A nil stored under an interface-typed key reads back as nil, not as an error.
func TestCached_NilUnderAnInterfaceKey(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	store := mapCache{}
	BindDataCache(rc, store)
	key := NewKey[fmt.Stringer]("s")
	calls := 0
	fetch := func(context.Context) (fmt.Stringer, error) { calls++; return nil, nil }
	for i := range 2 {
		v, err := Cached(rc, key, 0, nil, fetch)
		if err != nil || v != nil {
			t.Errorf("call %d = %v, %v; want nil, nil", i, v, err)
		}
	}
	if calls != 1 {
		t.Errorf("fetched %d times, want once: the stored nil was not found", calls)
	}
}

// wrongStore answers every Load with a value of its own choosing.
type wrongStore struct{ value CacheValue }

func (w wrongStore) Load(context.Context, string, time.Duration, []string, func(context.Context) (CacheValue, error), func([]byte) (CacheValue, error)) (CacheValue, bool, error) {
	return w.value, false, nil
}

type rawString string

func (r rawString) Encode() ([]byte, error) { return []byte(r), nil }

// A store handing back a value of another type is a bug in the store; Cached
// says so rather than returning an empty value as if all were well.
func TestCached_AStoreReturningAnotherTypeIsAnError(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	BindDataCache(rc, wrongStore{value: rawString("not an int")})
	v, err := Cached(rc, NewKey[int]("k"), 0, nil, func(context.Context) (int, error) { return 7, nil })
	if err == nil {
		t.Fatalf("Cached = %d, nil; want an error naming the store's mistake", v)
	}
	if !strings.Contains(err.Error(), `"k"`) {
		t.Errorf("error %q does not name the key", err)
	}
}

// With no store, or skipping it, Cached is Once: shared within the render only.
func TestCached_WithoutAStoreIsOnce(t *testing.T) {
	for _, skip := range []bool{false, true} {
		rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
		store := mapCache{}
		if skip {
			BindDataCache(rc, store)
			SkipDataCache(rc)
		}
		calls := 0
		fetch := func(context.Context) (int, error) { calls++; return calls, nil }
		Cached(rc, NewKey[int]("k"), 0, nil, fetch)
		Cached(rc, NewKey[int]("k"), 0, nil, fetch)
		if calls != 1 {
			t.Errorf("skip=%v: fetched %d times in one render, want once", skip, calls)
		}
		if len(store) != 0 {
			t.Errorf("skip=%v: the store was written", skip)
		}
	}
}

// A store key names its type the same way in every process, so a value written
// by one run is found by the next; and differently once the type's shape
// changes, so a value written for the old shape is not decoded into the new.
func TestStoreKey_StableAndShapeSensitive(t *testing.T) {
	type post struct{ Title string }
	a := storeKey(NewKey[post]("p").id())
	if b := storeKey(NewKey[post]("p").id()); a != b {
		t.Fatalf("one key, two store keys: %q / %q", a, b)
	}
	if strings.Contains(a, "0x") {
		t.Errorf("store key %q carries a pointer, which no other process shares", a)
	}
	changed := func() string {
		type post struct {
			Title string
			Body  string
		}
		return storeKey(NewKey[post]("p").id())
	}()
	if changed == a {
		t.Errorf("a post with a new field has the old post's store key %q", a)
	}
	tagged := func() string {
		type post struct {
			Title string `json:"headline"`
		}
		return storeKey(NewKey[post]("p").id())
	}()
	if tagged == a {
		t.Errorf("a renamed json field kept the store key %q", a)
	}
}

// A self-referencing type has a finite identity.
func TestStoreKey_RecursiveType(t *testing.T) {
	type node struct {
		Name     string
		Children []*node
	}
	if key := storeKey(NewKey[node]("n").id()); key == "" {
		t.Fatal("empty store key")
	}
}

// What Cached stores encodes as JSON and decodes back into the key's type.
func TestCached_ValuesRoundTripThroughJSON(t *testing.T) {
	type post struct{ Title string }
	encoded, err := cachedValue[post]{value: post{Title: "hi"}}.Encode()
	if err != nil || string(encoded) != `{"Title":"hi"}` {
		t.Fatalf("Encode = %s, %v", encoded, err)
	}
	decoded, err := decodeCached[post](encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded.(cachedValue[post]).value; got.Title != "hi" {
		t.Errorf("decoded %+v", got)
	}
}

// An interface-typed value cannot be decoded from JSON — nothing says which
// concrete type to make — so it is not encoded either: kept in memory only.
func TestCached_InterfaceKeysAreNotPersisted(t *testing.T) {
	if _, err := (cachedValue[fmt.Stringer]{}).Encode(); err == nil {
		t.Error("an interface-typed value encoded")
	}
	if _, err := decodeCached[fmt.Stringer]([]byte(`null`)); err == nil {
		t.Error("an interface-typed value decoded")
	}
}

// persistHidden has a field JSON never writes.
type persistHidden struct {
	Title string
	draft bool
}

// persistSkipped has a field JSON is told to skip.
type persistSkipped struct {
	Title  string
	Secret string `json:"-"`
}

// persistNested holds an interface a few levels down.
type persistNested struct {
	Items []map[string]persistBlock
}

type persistBlock struct{ Body fmt.Stringer }

// persistCustom has unexported state but writes and reads itself.
type persistCustom struct{ n int }

func (c persistCustom) MarshalJSON() ([]byte, error) { return []byte(fmt.Sprint(c.n)), nil }

func (c *persistCustom) UnmarshalJSON(data []byte) error {
	_, err := fmt.Sscan(string(data), &c.n)
	return err
}

type persistPlain struct {
	Title   string
	At      time.Time
	Tags    []string
	Authors map[string]*persistPlain
	Custom  persistCustom
	Ptr     *persistCustom
}

// A value JSON would bring back different — an unexported field zeroed, a
// skipped field dropped, an interface turned into maps and float64s — is not
// encoded: kept in memory only rather than restored wrong after a restart.
func TestCached_TypesThatDoNotRoundTripAreNotPersisted(t *testing.T) {
	refused := map[string]func() ([]byte, error){
		"unexported field": cachedValue[persistHidden]{}.Encode,
		"json:\"-\" field": cachedValue[persistSkipped]{}.Encode,
		"nested interface": cachedValue[persistNested]{}.Encode,
		"slice of them":    cachedValue[[]persistHidden]{}.Encode,
	}
	for name, encode := range refused {
		if _, err := encode(); err == nil {
			t.Errorf("%s: encoded", name)
		}
	}
	accepted := map[string]func() ([]byte, error){
		"plain struct":     cachedValue[persistPlain]{}.Encode,
		"slice of them":    cachedValue[[]persistPlain]{}.Encode,
		"map of them":      cachedValue[map[string]persistPlain]{}.Encode,
		"time.Time":        cachedValue[time.Time]{}.Encode,
		"custom marshaler": cachedValue[persistCustom]{}.Encode,
		"string":           cachedValue[string]{}.Encode,
	}
	for name, encode := range accepted {
		if _, err := encode(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// staleCache serves every key as a stale "old".
type staleCache struct{}

func (staleCache) Load(context.Context, string, time.Duration, []string, func(context.Context) (CacheValue, error), func([]byte) (CacheValue, error)) (CacheValue, bool, error) {
	return cachedValue[string]{value: "old"}, true, nil
}

// A render that read a stale value says so, so it is not cached as if fresh.
func TestCached_RecordsAStaleRead(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	BindDataCache(rc, mapCache{})
	Cached(rc, NewKey[string]("a"), 0, nil, func(context.Context) (string, error) { return "a", nil })
	if ServedStale(rc) {
		t.Fatal("a fresh read was recorded as stale")
	}
	BindDataCache(rc, staleCache{})
	if v, err := Cached(rc, NewKey[string]("b"), 0, nil, func(context.Context) (string, error) { return "new", nil }); v != "old" || err != nil {
		t.Fatalf("Cached = %q, %v", v, err)
	}
	if !ServedStale(rc) {
		t.Error("a stale read was not recorded")
	}
}
