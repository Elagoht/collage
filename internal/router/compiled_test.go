package router

import (
	"errors"
	"testing"
)

func TestCompilePattern_RefusesWhatRegistrationRefuses(t *testing.T) {
	for _, p := range []string{"", "old", "/a//b", "/a/{", "/{x...}/b", "/{}"} {
		if _, err := CompilePattern(p); !errors.Is(err, ErrInvalidPattern) {
			t.Errorf("CompilePattern(%q) = %v, want ErrInvalidPattern", p, err)
		}
	}
}

func TestPattern_KeyFoldsSlashAndPlaceholderNames(t *testing.T) {
	for _, pair := range [][2]string{
		{"/old", "/old/"},
		{"/blog/{slug}", "/blog/{x}"},
		{"/docs/{rest...}", "/docs/{path...}/"},
		{"/p/{id}.md", "/p/{n}.md"},
		{"/", "/"},
	} {
		a, err := CompilePattern(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		b, err := CompilePattern(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if a.Key() != b.Key() {
			t.Errorf("Key(%q) = %q, Key(%q) = %q; want equal", pair[0], a.Key(), pair[1], b.Key())
		}
	}
	a, _ := CompilePattern("/p/{id}.md")
	b, _ := CompilePattern("/p/{id}.json")
	if a.Key() == b.Key() {
		t.Errorf("%q and %q share a key", "/p/{id}.md", "/p/{id}.json")
	}
}

func TestPattern_Matches(t *testing.T) {
	for _, c := range []struct {
		pattern, path string
		want          bool
	}{
		{"/docs/{rest...}", "/docs/x/", true},
		{"/docs/{rest...}", "/docs/x/y", true},
		{"/docs/{rest...}", "/docs/", false},
		{"/blog/{slug}", "/blog/a", true},
		{"/blog/{slug}", "/blog/a/b", false},
		{"/p/{id}.md", "/p/1.md", true},
		{"/p/{id}.md", "/p/1.json", false},
		{"/a", "/a/", true},
		{"/a", "/b", false},
		{"/", "/", true},
	} {
		p, err := CompilePattern(c.pattern)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Matches(c.path); got != c.want {
			t.Errorf("%q.Matches(%q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestPattern_CheckCaptures(t *testing.T) {
	p, _ := CompilePattern("/old/{slug}/{rest...}")
	if err := p.CheckCaptures("/new/{slug}/{rest}"); err != nil {
		t.Errorf("CheckCaptures: %v", err)
	}
	if err := p.CheckCaptures("https://example.com/{slug}"); err != nil {
		t.Errorf("CheckCaptures absolute: %v", err)
	}
	if err := p.CheckCaptures("/new/{id}"); !errors.Is(err, ErrUnsubstitutedPlaceholder) {
		t.Errorf("CheckCaptures = %v, want ErrUnsubstitutedPlaceholder", err)
	}
	if p.Literal() {
		t.Errorf("Literal of a placeholder pattern")
	}
	lit, _ := CompilePattern("/a/b")
	if !lit.Literal() {
		t.Errorf("Literal(/a/b) = false")
	}
}
