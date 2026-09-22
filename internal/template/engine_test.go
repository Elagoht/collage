package template

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"strings"
	"testing"
)

// These tests cover the Engine contract's central promise: a template's function
// names must be fixed at parse time, but RenderWithFuncs can rebind what a
// parse-time name does for a single render, on a private clone. This is what makes
// {{slot "name"}} per-render rather than global.

func TestRenderWithFuncs_SlotOverride(t *testing.T) {
	engine, err := NewHTML(HTMLConfig{Root: "testdata/valid", Extension: ".html"})
	if err != nil {
		t.Fatalf("NewHTML() error = %v", err)
	}

	t.Run("without override, placeholder reports ErrSlotOutsideRender", func(t *testing.T) {
		var buf bytes.Buffer
		err := engine.Render(context.Background(), &buf, "slot.html", nil)
		if !errors.Is(err, ErrSlotOutsideRender) {
			t.Fatalf("Render() error = %v, want ErrSlotOutsideRender", err)
		}
		if buf.Len() != 0 {
			t.Errorf("buffer = %q, want empty on error", buf.String())
		}
	})

	t.Run("with override, real implementation runs for this render only", func(t *testing.T) {
		funcs := template.FuncMap{
			"slot": func(name string) (template.HTML, error) {
				return template.HTML("<em>" + name + "</em>"), nil
			},
		}
		var buf bytes.Buffer
		if err := engine.RenderWithFuncs(context.Background(), &buf, "slot.html", nil, funcs); err != nil {
			t.Fatalf("RenderWithFuncs() error = %v", err)
		}
		want := "<div><em>content</em></div>\n"
		if buf.String() != want {
			t.Errorf("RenderWithFuncs() output = %q, want %q", buf.String(), want)
		}

		// A concurrent render without the override must still see the placeholder:
		// the override must not have leaked onto the shared template set.
		var buf2 bytes.Buffer
		err := engine.Render(context.Background(), &buf2, "slot.html", nil)
		if !errors.Is(err, ErrSlotOutsideRender) {
			t.Fatalf("Render() after override error = %v, want ErrSlotOutsideRender (override leaked onto shared set)", err)
		}
	})
}

func TestNewHTML_UndeclaredFuncName_FailsAtParse(t *testing.T) {
	// A template calling a function name that was never in the FuncMap at parse
	// time must fail construction, not render. RenderWithFuncs cannot introduce a
	// brand-new name after the fact — only replace one that already exists.
	_, err := NewHTML(HTMLConfig{Root: "testdata/undeclaredfunc", Extension: ".html"})
	if err == nil {
		t.Fatal("NewHTML() error = nil, want a parse error for the undeclared function name")
	}
	if !strings.Contains(err.Error(), "undeclaredFn") {
		t.Errorf("NewHTML() error = %v, want it to mention the undeclared function name", err)
	}
}
