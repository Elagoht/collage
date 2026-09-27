package render

import (
	"sync/atomic"
	"testing"

	"github.com/Elagoht/collage/internal/template"
	"github.com/Elagoht/collage/internal/types"
)

// countingEngine counts AddSource calls on a real engine.
type countingEngine struct {
	*template.HTMLEngine
	adds atomic.Int32
}

func (c *countingEngine) AddSource(name, src string) error {
	c.adds.Add(1)
	return c.HTMLEngine.AddSource(name, src)
}

// An inline source that fails to parse at render time — one a resolver returned,
// which registration never saw — fails once and is not parsed again: every retry
// takes the engine's write lock and clones the whole template set, per render.
func TestRender_BrokenInlineSourceIsNotReparsedEveryRender(t *testing.T) {
	html, err := template.NewHTML(template.HTMLConfig{Root: "testdata", Extension: ".html"})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	counting := &countingEngine{HTMLEngine: html}
	engine := New(counting, Options{})
	broken := &types.Fragment{Name: "broken", Source: `{{.Nope`}
	for i := 0; i < 3; i++ {
		if _, err := renderPage(t, engine, pageWith(broken)); err != nil {
			t.Logf("render %d: %v", i, err)
		}
	}
	if n := counting.adds.Load(); n != 1 {
		t.Fatalf("AddSource called %d times over three renders, want 1", n)
	}
	result, _ := renderPage(t, engine, pageWith(broken))
	if result != nil {
		meta := fragmentMetadata(t, result, "broken")
		if meta.Err == nil {
			t.Fatal("a remembered parse failure rendered as a success")
		}
	}
}
