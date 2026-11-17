package template

import (
	"html/template"
	"strings"
	"testing"
	"testing/fstest"
)

// Escape reports what html/template would refuse on the first render, without
// calling a single template function, and leaves the set renderable.
func TestEscape_ReportsWhatTheFirstRenderWouldRefuse(t *testing.T) {
	called := false
	e, err := NewHTML(HTMLConfig{
		FS: fstest.MapFS{
			"t/ok.html":  {Data: []byte(`<p>{{boom}}</p>`)},
			"t/bad.html": {Data: []byte(`<script>var x = "{{boom}}`)},
		},
		Root: "t", Extension: ".html",
		Funcs: template.FuncMap{"boom": func() string { called = true; return "" }},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Escape("ok.html"); err != nil {
		t.Fatalf("Escape(ok.html) = %v", err)
	}
	err = e.Escape("bad.html")
	if err == nil || !strings.Contains(err.Error(), "bad.html") {
		t.Fatalf("Escape(bad.html) = %v, want an error naming the template", err)
	}
	if called {
		t.Fatal("Escape called a template function")
	}
	if err := e.Escape("missing.html"); err == nil {
		t.Fatal("Escape of a template that does not exist succeeded")
	}
}
