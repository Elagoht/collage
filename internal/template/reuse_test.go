package template

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

// A render executes a copy of the template set that it alone holds, and the copy
// is kept for a later render instead of being made again for every fragment. What
// one render bound onto its copy must not reach the next render to use it: a
// function bound to reader A's request, run in reader B's page, writes A's token
// or name into B's answer.

func reuseEngine(t *testing.T) *HTMLEngine {
	t.Helper()
	e, err := NewHTML(HTMLConfig{
		FS: fstest.MapFS{
			"t/who.html":   {Data: []byte(`<p>{{who}}</p>`)},
			"t/other.html": {Data: []byte(`<p>{{.}}</p>`)},
		},
		Root:      "t",
		Extension: ".html",
		Funcs:     template.FuncMap{"who": func() string { return "nobody" }},
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func renderWho(t *testing.T, e *HTMLEngine, funcs template.FuncMap) string {
	t.Helper()
	var buf bytes.Buffer
	if err := e.RenderWithFuncs(context.Background(), &buf, "who.html", nil, funcs); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// The property the reuse rests on: a function rebound onto a copy that has
// already executed is the one its next execution calls.
func TestHTMLTemplate_FuncsRebindAfterExecute(t *testing.T) {
	set := template.Must(template.New("").Funcs(template.FuncMap{"who": func() string { return "a" }}).Parse(`{{who}}`))
	var first, second bytes.Buffer
	if err := set.Execute(&first, nil); err != nil {
		t.Fatal(err)
	}
	set.Funcs(template.FuncMap{"who": func() string { return "b" }})
	if err := set.Execute(&second, nil); err != nil {
		t.Fatal(err)
	}
	if first.String() != "a" || second.String() != "b" {
		t.Fatalf("executions wrote %q then %q, want a then b", first.String(), second.String())
	}
}

func TestRender_ABoundFunctionDoesNotOutliveItsRender(t *testing.T) {
	e := reuseEngine(t)
	if got := renderWho(t, e, template.FuncMap{"who": func() string { return "reader-a" }}); got != "<p>reader-a</p>" {
		t.Fatalf("first render: %q", got)
	}
	for range 3 {
		if got := renderWho(t, e, nil); got != "<p>nobody</p>" {
			t.Fatalf("a render binding nothing ran the last render's function: %q", got)
		}
	}
	if got := renderWho(t, e, template.FuncMap{"upper": strings.ToLower}); got != "<p>nobody</p>" {
		t.Fatalf("a render binding other names ran the last render's function: %q", got)
	}
}

// A render that failed hands back nothing a later one picks up.
func TestRender_AFailedRenderLeavesNothingBehind(t *testing.T) {
	e := reuseEngine(t)
	var buf bytes.Buffer
	err := e.RenderWithFuncs(context.Background(), &buf, "who.html", nil, template.FuncMap{
		"who": func() (string, error) { return "reader-a", errors.New("refused") },
	})
	if err == nil {
		t.Fatal("the failing render succeeded")
	}
	if got := renderWho(t, e, nil); got != "<p>nobody</p>" {
		t.Fatalf("after a failed render: %q", got)
	}
}

// Many renders at once, each with its own binding, each seeing only its own.
func TestRender_ConcurrentRendersSeeOnlyTheirOwnFunctions(t *testing.T) {
	e := reuseEngine(t)
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for g := range 64 {
		wg.Go(func() {
			want := fmt.Sprintf("reader-%d", g)
			for range 50 {
				var buf bytes.Buffer
				var funcs template.FuncMap
				if g%4 != 0 {
					funcs = template.FuncMap{"who": func() string { return want }}
				} else {
					want = "nobody"
				}
				if err := e.RenderWithFuncs(context.Background(), &buf, "who.html", nil, funcs); err != nil {
					errs <- err
					return
				}
				if got := buf.String(); got != "<p>"+want+"</p>" {
					errs <- fmt.Errorf("goroutine %d got %q, want %q", g, got, want)
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// A template added after renders have run is in every later render's set.
func TestRender_ASourceAddedAfterRendersIsSeen(t *testing.T) {
	e := reuseEngine(t)
	renderWho(t, e, nil)
	if err := e.AddSource("inline/late", `<b>{{who}}</b>`); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := e.RenderWithFuncs(context.Background(), &buf, "inline/late", nil, nil); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "<b>nobody</b>" {
		t.Fatalf("late source: %q", buf.String())
	}
}

// What a render costs follows the template it runs, not every template the
// project holds.
func BenchmarkRender_LargeSet(b *testing.B) {
	files := fstest.MapFS{"t/page.html": {Data: []byte(`<p>{{.}}</p>`)}}
	for i := range 200 {
		files[fmt.Sprintf("t/unused/%d.html", i)] = &fstest.MapFile{Data: []byte(`<section>{{if .}}<a href="/?q={{.}}">{{.}}</a>{{end}}</section>`)}
	}
	e, err := NewHTML(HTMLConfig{FS: files, Root: "t", Extension: ".html"})
	if err != nil {
		b.Fatal(err)
	}
	funcs := template.FuncMap{"upper": strings.ToUpper}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		if err := e.RenderWithFuncs(context.Background(), &buf, "page.html", "x", funcs); err != nil {
			b.Fatal(err)
		}
	}
}
