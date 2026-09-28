package router

import (
	"errors"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// Next.js: test/e2e/repeated-forward-slashes-error ("should log error when href
// has repeated forward-slashes") and test/e2e/invalid-href. A link with "//" or a
// backslash in it is refused where it is built, because a browser reads "//x" and
// "/\x" as another host. A value holding either is escaped, or refused where it
// would make an empty segment, so no built link begins with them.
func TestNextjs_ABuiltLinkNeverHasARepeatedSlash(t *testing.T) {
	for _, c := range []struct {
		pattern string
		params  map[string]string
		want    string
	}{
		{"/{slug}", map[string]string{"slug": `\google.com`}, "/%5Cgoogle.com"},
		{"/{slug}", map[string]string{"slug": `\\google.com`}, "/%5C%5Cgoogle.com"},
		{"/{rest...}", map[string]string{"rest": `\/google.com`}, "/%5C/google.com"},
		{"/{rest...}", map[string]string{"rest": "hello/world"}, "/hello/world"},
		{"/{rest...}", map[string]string{"rest": "/google.com"}, "/google.com"},
	} {
		got, err := BuildPath(c.pattern, c.params)
		if err != nil || got != c.want {
			t.Errorf("BuildPath(%q, %v) = %q, %v, want %q", c.pattern, c.params, got, err, c.want)
		}
	}

	for _, c := range []struct {
		pattern string
		params  map[string]string
	}{
		{"/{rest...}", map[string]string{"rest": "hello//world"}},
		{"/{rest...}", map[string]string{"rest": "a/../../b"}},
		{"/{rest...}", map[string]string{"rest": "a/./b"}},
		{"/{slug}", map[string]string{"slug": "/google.com"}},
		{"/{slug}", map[string]string{"slug": "//google.com"}},
	} {
		got, err := BuildPath(c.pattern, c.params)
		if !errors.Is(err, types.ErrRouteParams) {
			t.Errorf("BuildPath(%q, %v) = %q, %v, want ErrRouteParams", c.pattern, c.params, got, err)
		}
		if strings.HasPrefix(got, "//") || strings.HasPrefix(got, `/\`) {
			t.Errorf("BuildPath(%q, %v) = %q", c.pattern, c.params, got)
		}
	}
}
