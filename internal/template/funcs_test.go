package template

import (
	"errors"
	"html/template"
	"testing"
	"time"
)

func TestDefaultFuncs_HasAllDocumentedNames(t *testing.T) {
	want := []string{
		"slot", "hoist", "asset", "csrfToken", "safeHTML", "safeURL", "dict", "default",
		"upper", "lower", "title", "join", "formatTime", "pageURL", "pageURLIn", "localeURL", "stylesheet",
		"fragmentURL", "fragmentURLIn",
	}
	funcs := DefaultFuncs()
	for _, name := range want {
		if _, ok := funcs[name]; !ok {
			t.Errorf("DefaultFuncs()[%q] missing", name)
		}
	}
	if len(funcs) != len(want) {
		t.Errorf("DefaultFuncs() has %d entries, want %d", len(funcs), len(want))
	}
}

func TestAssetPlaceholder(t *testing.T) {
	url, err := assetPlaceholder("/static/app.css")
	if url != "" {
		t.Errorf("assetPlaceholder() url = %q, want empty", url)
	}
	if !errors.Is(err, ErrAssetOutsideRender) {
		t.Fatalf("assetPlaceholder() error = %v, want ErrAssetOutsideRender", err)
	}
}

func TestSlotPlaceholder(t *testing.T) {
	html, err := slotPlaceholder("content")
	if html != "" {
		t.Errorf("slotPlaceholder() html = %q, want empty", html)
	}
	if !errors.Is(err, ErrSlotOutsideRender) {
		t.Fatalf("slotPlaceholder() error = %v, want ErrSlotOutsideRender", err)
	}
}

func TestSafeHTML(t *testing.T) {
	got := safeHTML("<b>hi</b>")
	if got != template.HTML("<b>hi</b>") {
		t.Errorf("safeHTML() = %v, want <b>hi</b>", got)
	}
}

func TestSafeURL(t *testing.T) {
	got := safeURL("https://example.com/x?y=1")
	if got != template.URL("https://example.com/x?y=1") {
		t.Errorf("safeURL() = %v, want https://example.com/x?y=1", got)
	}
}

// TestDict is not a data table (unlike the rest of this file) because a table field
// holding dict's variadic arguments or expected map would itself have to be typed
// []any / map[string]any — a second declaration of the forbidden type beyond dict's
// own permitted signature. Each case instead calls dict with literal arguments, so no
// test code declares "any" anywhere.
func TestDict(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got, err := dict()
		if err != nil {
			t.Fatalf("dict() unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("dict() = %v, want empty map", got)
		}
	})

	t.Run("two pairs", func(t *testing.T) {
		got, err := dict("a", 1, "b", "two")
		if err != nil {
			t.Fatalf("dict() unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("dict() = %v, want 2 entries", got)
		}
		if got["a"] != 1 {
			t.Errorf(`dict()["a"] = %v, want 1`, got["a"])
		}
		if got["b"] != "two" {
			t.Errorf(`dict()["b"] = %v, want "two"`, got["b"])
		}
	})

	t.Run("odd argument count", func(t *testing.T) {
		_, err := dict("a")
		if !errors.Is(err, ErrDictOddArgs) {
			t.Fatalf("dict() error = %v, want ErrDictOddArgs", err)
		}
	})

	t.Run("non-string key", func(t *testing.T) {
		_, err := dict(1, "value")
		if !errors.Is(err, ErrDictKeyNotString) {
			t.Fatalf("dict() error = %v, want ErrDictKeyNotString", err)
		}
	})
}

func TestDefaultValue(t *testing.T) {
	tests := []struct {
		name     string
		fallback string
		value    string
		want     string
	}{
		{name: "empty value uses fallback", fallback: "fallback", value: "", want: "fallback"},
		{name: "non-empty value wins", fallback: "fallback", value: "actual", want: "actual"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultValue(tt.fallback, tt.value); got != tt.want {
				t.Errorf("defaultValue(%q, %q) = %q, want %q", tt.fallback, tt.value, got, tt.want)
			}
		})
	}
}

func TestUpperLower(t *testing.T) {
	funcs := DefaultFuncs()
	tests := []struct {
		funcName string
		in       string
		want     string
	}{
		{funcName: "upper", in: "Hello", want: "HELLO"},
		{funcName: "lower", in: "Hello", want: "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.funcName, func(t *testing.T) {
			fn, ok := funcs[tt.funcName].(func(string) string)
			if !ok {
				t.Fatalf("DefaultFuncs()[%q] is not a func(string) string", tt.funcName)
			}
			if got := fn(tt.in); got != tt.want {
				t.Errorf("%s(%q) = %q, want %q", tt.funcName, tt.in, got, tt.want)
			}
		})
	}
}

func TestTitle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "simple words", in: "hello world", want: "Hello World"},
		{name: "already upper", in: "HELLO WORLD", want: "Hello World"},
		{name: "punctuation preserved", in: "hello, world!", want: "Hello, World!"},
		{name: "empty", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := title(tt.in); got != tt.want {
				t.Errorf("title(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestJoin(t *testing.T) {
	tests := []struct {
		name  string
		sep   string
		items []string
		want  string
	}{
		{name: "comma", sep: ", ", items: []string{"a", "b", "c"}, want: "a, b, c"},
		{name: "empty slice", sep: ", ", items: nil, want: ""},
		{name: "single item", sep: ", ", items: []string{"a"}, want: "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := join(tt.sep, tt.items); got != tt.want {
				t.Errorf("join(%q, %v) = %q, want %q", tt.sep, tt.items, got, tt.want)
			}
		})
	}
}

func TestFormatTime(t *testing.T) {
	ts := time.Date(2026, 9, 22, 14, 30, 0, 0, time.UTC)
	tests := []struct {
		name   string
		layout string
		want   string
	}{
		{name: "date only", layout: "2006-01-02", want: "2026-09-22"},
		{name: "date and time", layout: time.RFC3339, want: "2026-09-22T14:30:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatTime(ts, tt.layout); got != tt.want {
				t.Errorf("formatTime(%v, %q) = %q, want %q", ts, tt.layout, got, tt.want)
			}
		})
	}
}
