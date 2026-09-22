package cache

import "testing"

func TestKey_Deterministic(t *testing.T) {
	in := KeyInput{
		Path:   "/blog/post",
		Locale: "en",
		Params: map[string]string{"page": "2", "sort": "new"},
		Vary:   []string{"device:mobile", "theme:dark"},
	}

	first := Key(in)
	second := Key(in)
	if first != second {
		t.Fatalf("Key(in) not deterministic: %q != %q", first, second)
	}
}

func TestKey_DeterministicRegardlessOfMapInsertionOrder(t *testing.T) {
	a := KeyInput{
		Path:   "/blog/post",
		Locale: "en",
		Params: map[string]string{},
		Vary:   []string{"b", "a", "c"},
	}
	a.Params["page"] = "2"
	a.Params["sort"] = "new"
	a.Params["tag"] = "go"

	b := KeyInput{
		Path:   "/blog/post",
		Locale: "en",
		Params: map[string]string{},
		Vary:   []string{"c", "a", "b"},
	}
	b.Params["tag"] = "go"
	b.Params["page"] = "2"
	b.Params["sort"] = "new"

	if Key(a) != Key(b) {
		t.Fatalf("Key differs for inputs that are equal apart from insertion order: %q != %q", Key(a), Key(b))
	}
}

func TestKey_HasV1Prefix(t *testing.T) {
	got := Key(KeyInput{Path: "/"})
	const want = "v1:"
	if len(got) < len(want) || got[:len(want)] != want {
		t.Fatalf("Key(...) = %q, want prefix %q", got, want)
	}
}

// TestKey_CollisionResistance checks that structurally different KeyInputs never
// serialise to the same key, covering the classes of ambiguity naive concatenation
// is prone to: boundary-shifting between Path and Locale, and moving a value
// between the Params and Vary sections.
func TestKey_CollisionResistance(t *testing.T) {
	tests := []struct {
		name string
		a    KeyInput
		b    KeyInput
	}{
		{
			name: "path/locale boundary shift",
			a:    KeyInput{Path: "/ab", Locale: "c"},
			b:    KeyInput{Path: "/a", Locale: "bc"},
		},
		{
			name: "value moved from params into vary",
			a:    KeyInput{Path: "/", Params: map[string]string{"ab": "c"}},
			b:    KeyInput{Path: "/", Vary: []string{"ab", "c"}},
		},
		{
			name: "param key/value boundary shift",
			a:    KeyInput{Path: "/", Params: map[string]string{"ab": "cd"}},
			b:    KeyInput{Path: "/", Params: map[string]string{"a": "bcd"}},
		},
		{
			name: "vary entry split differently",
			a:    KeyInput{Path: "/", Vary: []string{"ab", "cd"}},
			b:    KeyInput{Path: "/", Vary: []string{"abc", "d"}},
		},
		{
			name: "different locale, everything else equal",
			a:    KeyInput{Path: "/x", Locale: "en"},
			b:    KeyInput{Path: "/x", Locale: "tr"},
		},
		{
			name: "empty params vs one empty-string param",
			a:    KeyInput{Path: "/"},
			b:    KeyInput{Path: "/", Params: map[string]string{"": ""}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ka, kb := Key(tt.a), Key(tt.b)
			if ka == kb {
				t.Fatalf("Key collision: Key(%+v) == Key(%+v) == %q", tt.a, tt.b, ka)
			}
		})
	}
}

func TestETag_StableAndQuoted(t *testing.T) {
	content := []byte("<html>hello</html>")

	first := ETag(content)
	second := ETag(content)
	if first != second {
		t.Fatalf("ETag not stable across calls: %q != %q", first, second)
	}

	if len(first) < 2 || first[0] != '"' || first[len(first)-1] != '"' {
		t.Fatalf("ETag(...) = %q, want surrounding double quotes", first)
	}

	other := ETag([]byte("<html>different</html>"))
	if first == other {
		t.Fatalf("ETag(a) == ETag(b) for different content: %q", first)
	}
}

func TestETagMatch(t *testing.T) {
	const etag = `"abc123"`

	tests := []struct {
		name        string
		ifNoneMatch string
		etag        string
		want        bool
	}{
		{"wildcard matches", "*", etag, true},
		{"wildcard with surrounding whitespace matches", "  *  ", etag, true},
		{"exact match", `"abc123"`, etag, true},
		{"weak prefix matches", `W/"abc123"`, etag, true},
		{"surrounding whitespace ignored", `  "abc123"  `, etag, true},
		{"list containing match", `"zzz", "abc123"`, etag, true},
		{"list containing weak match with whitespace", `"zzz" , W/"abc123"`, etag, true},
		{"no match", `"other"`, etag, false},
		{"list with no match", `"one", "two", "three"`, etag, false},
		{"empty header never matches", "", etag, false},
		{"empty etag never matches", `"abc123"`, "", false},
		{"both empty never match", "", "", false},
		{"stray commas do not panic and do not match", ",,,", etag, false},
		{"malformed missing quotes does not panic", "abc123", etag, false},
		{"trailing comma with empty item does not panic", `"abc123",`, etag, true},
		{"weak wildcard is not the wildcard token", `W/*`, etag, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ETagMatch(tt.ifNoneMatch, tt.etag); got != tt.want {
				t.Errorf("ETagMatch(%q, %q) = %v, want %v", tt.ifNoneMatch, tt.etag, got, tt.want)
			}
		})
	}
}
