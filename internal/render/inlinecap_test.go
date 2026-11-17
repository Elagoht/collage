package render

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Elagoht/collage/internal/template"
	"github.com/Elagoht/collage/internal/types"
)

// A slot resolver can return a fragment with an inline source no render has seen.
// Each new one takes the engine's write lock, copies the whole template set, and
// is kept for good, so a resolver that builds a source from request data grows the
// set without bound. Past a cap the render fails with an error saying why.
func TestRender_InlineSourcesFromAResolverAreCapped(t *testing.T) {
	html, err := template.NewHTML(template.HTMLConfig{Root: "testdata", Extension: ".html"})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	engine := New(html, Options{})

	var refused error
	for i := range maxRuntimeSources + 20 {
		frag := &types.Fragment{Name: "dyn", Source: fmt.Sprintf(`<p>%d</p>`, i)}
		result, renderErr := renderPage(t, engine, pageWith(frag))
		if renderErr != nil {
			t.Fatalf("render %d: %v", i, renderErr)
		}
		if meta := fragmentMetadata(t, result, "dyn"); meta.Err != nil {
			refused = meta.Err
			if i < maxRuntimeSources {
				t.Fatalf("source %d refused below the cap: %v", i, meta.Err)
			}
		}
	}
	if !errors.Is(refused, ErrTooManyInlineSources) {
		t.Fatalf("past the cap the fragment failed with %v, want ErrTooManyInlineSources", refused)
	}
}
