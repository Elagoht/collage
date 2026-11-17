package template

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"testing"
	"testing/fstest"
)

func failingSet(tb testing.TB) *HTMLEngine {
	tb.Helper()
	files := fstest.MapFS{
		"t/page.html": {Data: []byte(`<p>{{fail}}</p>`)},
	}
	for i := range 300 {
		files[fmt.Sprintf("t/unused/%d.html", i)] = &fstest.MapFile{Data: []byte(`<section>{{if .}}<a href="/?q={{.}}">{{.}}</a>{{end}}</section>`)}
	}
	e, err := NewHTML(HTMLConfig{
		FS: files, Root: "t", Extension: ".html",
		Funcs: template.FuncMap{"fail": func() (string, error) { return "", errors.New("not found") }},
	})
	if err != nil {
		tb.Fatal(err)
	}
	return e
}

// A render that fails with an ordinary error — a page's data not found — keeps its
// copy of the set for the next render. Dropping it made every 404 pay for copying
// and escaping every template again, which an unauthenticated loop of random URLs
// could ask for as often as it liked.
func TestRender_AFailedRenderKeepsItsCopyOfTheSet(t *testing.T) {
	e := failingSet(t)
	const renders = 200
	for range renders {
		var buf bytes.Buffer
		if err := e.RenderWithFuncs(context.Background(), &buf, "page.html", nil, nil); err == nil {
			t.Fatal("the failing render succeeded")
		}
	}
	// The race detector makes a sync.Pool drop a quarter of what it is given, so
	// "never" cannot be asserted; "far fewer than every render" can.
	if got := e.clones.Load(); got > renders/2 {
		t.Fatalf("%d of %d failed renders copied the template set; a failure should hand its copy back", got, renders)
	}
}

func BenchmarkRender_FailedRender(b *testing.B) {
	e := failingSet(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		_ = e.RenderWithFuncs(context.Background(), &buf, "page.html", nil, nil)
	}
}
