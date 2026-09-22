package render

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/template"
	"github.com/Elagoht/collage/internal/types"
)

// newEngine builds a SlotEngine over the testdata template set.
func newEngine(t *testing.T, opts Options) *SlotEngine {
	t.Helper()
	tmpl, err := template.NewHTML(template.HTMLConfig{Root: "testdata", Extension: ".html"})
	if err != nil {
		t.Fatalf("template.NewHTML() error = %v", err)
	}
	return New(tmpl, opts)
}

// fragment builds a fragment with no slots, no handler, and no fallback.
func fragment(name, path string) *types.Fragment {
	return &types.Fragment{Name: name, TemplatePath: path}
}

// declare adds slot to f's slot map and returns f, for building trees inline.
func declare(f *types.Fragment, slot *types.SlotDefinition) *types.Fragment {
	if f.Slots == nil {
		f.Slots = make(map[string]*types.SlotDefinition)
	}
	f.Slots[slot.Name] = slot
	return f
}

// bind binds each child to parent's slot in the order given, failing the test if the
// binding itself is rejected.
func bind(t *testing.T, parent *types.Fragment, slot string, children ...*types.Fragment) {
	t.Helper()
	for _, child := range children {
		if err := parent.Bind(slot, child); err != nil {
			t.Fatalf("Bind(%q, %q) error = %v", slot, child.Name, err)
		}
	}
}

// pageWith builds a single-content-fragment page.
func pageWith(root *types.Fragment, tags ...string) *types.Page {
	return &types.Page{Name: "test-page", ContentFragment: root, DependencyTags: tags}
}

// renderPage renders page for locale "en" with a background context.
func renderPage(t *testing.T, engine *SlotEngine, page *types.Page) (*Result, error) {
	t.Helper()
	rc := types.NewRenderContext(context.Background(), nil, page, "en", nil)
	return engine.Render(context.Background(), rc)
}

// dataHandler returns a handler yielding data and tags and no error.
func dataHandler(data map[string]string, tags ...string) types.DataHandlerFunc {
	return func(context.Context, *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		return data, tags, nil
	}
}

// failingHandler returns a handler that reports tags and then fails.
func failingHandler(err error, tags ...string) types.DataHandlerFunc {
	return func(context.Context, *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		return nil, tags, err
	}
}

func TestRender_SingleFragmentWithData(t *testing.T) {
	engine := newEngine(t, Options{})
	content := fragment("content", "simple.html")
	content.DataHandler = dataHandler(map[string]string{"Title": "Tom & Jerry"})

	result, err := renderPage(t, engine, pageWith(content))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	want := "<h1>Tom &amp; Jerry</h1>"
	if string(result.HTML) != want {
		t.Errorf("Render() HTML = %q, want %q", result.HTML, want)
	}
	if result.Degraded() {
		t.Error("Degraded() = true, want false for a clean render")
	}
	if got := result.Metadata.Fragments; len(got) != 1 || got[0].Name != "content" {
		t.Errorf("Metadata.Fragments = %+v, want one entry for %q", got, "content")
	}
	if result.Metadata.Page != "test-page" || result.Metadata.Locale != "en" {
		t.Errorf("Metadata page/locale = %q/%q, want %q/%q", result.Metadata.Page, result.Metadata.Locale, "test-page", "en")
	}
}

func TestRender_RejectsUnrenderableInput(t *testing.T) {
	engine := newEngine(t, Options{})

	tests := []struct {
		name     string
		rc       *types.RenderContext
		want     error
		wantPage string
	}{
		{
			name: "nil render context",
			rc:   nil,
			want: ErrNilRenderContext,
		},
		{
			name: "render context without a page",
			rc:   types.NewRenderContext(context.Background(), nil, nil, "en", nil),
			want: ErrNilRenderContext,
		},
		{
			name:     "page without any fragment",
			rc:       types.NewRenderContext(context.Background(), nil, &types.Page{Name: "empty"}, "en", nil),
			want:     ErrNoRootFragment,
			wantPage: "empty",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := engine.Render(context.Background(), test.rc)
			if !errors.Is(err, test.want) {
				t.Fatalf("Render() error = %v, want %v", err, test.want)
			}
			// Even a rejected request gets a Result: callers read Metadata
			// unconditionally and must not have to nil-check it first.
			if result == nil || result.Metadata == nil {
				t.Fatalf("Render() result = %+v, want a non-nil Result carrying metadata", result)
			}
			if result.HTML != nil {
				t.Errorf("Render() HTML = %q, want nil alongside an error", result.HTML)
			}
			if result.Metadata.Page != test.wantPage {
				t.Errorf("Metadata.Page = %q, want %q", result.Metadata.Page, test.wantPage)
			}
		})
	}
}

func TestRender_DependencyTagsAreDeduplicatedAndSorted(t *testing.T) {
	engine := newEngine(t, Options{})

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content", AllowMultiple: true})
	layout.DataHandler = dataHandler(nil, "nav", "user:1")

	first := fragment("first", "a.html")
	first.DataHandler = dataHandler(nil, "post:7", "nav", "")
	second := fragment("second", "b.html")
	second.DataHandler = dataHandler(nil, "post:7", "author:3")
	bind(t, layout, "content", first, second)

	result, err := renderPage(t, engine, pageWith(layout, "site", "nav"))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	want := []string{"author:3", "nav", "post:7", "site", "user:1"}
	if !slices.Equal(result.DependencyTags, want) {
		t.Errorf("DependencyTags = %v, want %v", result.DependencyTags, want)
	}
}

func TestRender_Degraded(t *testing.T) {
	boom := errors.New("collage: boom")

	tests := []struct {
		name string
		// build returns the page's root fragment.
		build func(t *testing.T) *types.Fragment
		want  bool
	}{
		{
			name:  "clean render",
			build: func(*testing.T) *types.Fragment { return fragment("content", "plain.html") },
			want:  false,
		},
		{
			name: "optional fragment failed",
			build: func(*testing.T) *types.Fragment {
				content := fragment("content", "plain.html")
				content.DataHandler = failingHandler(boom)
				return content
			},
			want: true,
		},
		{
			name: "optional fragment failed but its fallback rendered",
			build: func(*testing.T) *types.Fragment {
				content := fragment("content", "plain.html")
				content.DataHandler = failingHandler(boom)
				content.Fallback = fragment("content-fallback", "fallback.html")
				return content
			},
			want: true,
		},
		{
			name: "child fragment failed, parent fine",
			build: func(t *testing.T) *types.Fragment {
				layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
				child := fragment("child", "leaf.html")
				child.DataHandler = failingHandler(boom)
				bind(t, layout, "content", child)
				return layout
			},
			want: true,
		},
	}

	engine := newEngine(t, Options{})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := renderPage(t, engine, pageWith(test.build(t)))
			if err != nil {
				t.Fatalf("Render() error = %v, want the failure to be contained", err)
			}
			if got := result.Degraded(); got != test.want {
				t.Errorf("Degraded() = %v, want %v (fragments: %+v)", got, test.want, result.Metadata.Fragments)
			}
		})
	}

	t.Run("nil result and nil metadata are not degraded", func(t *testing.T) {
		var nilResult *Result
		if nilResult.Degraded() {
			t.Error("(*Result)(nil).Degraded() = true, want false")
		}
		if (&Result{}).Degraded() {
			t.Error("Result{}.Degraded() = true, want false")
		}
	})
}

func TestRender_MetadataTimings(t *testing.T) {
	engine := newEngine(t, Options{})

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
	child := fragment("child", "leaf.html")
	child.DataHandler = func(context.Context, *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		time.Sleep(2 * time.Millisecond)
		return nil, nil, nil
	}
	bind(t, layout, "content", child)

	result, err := renderPage(t, engine, pageWith(layout))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	timing := result.Metadata.Timing
	if timing.Total <= 0 {
		t.Errorf("Timing.Total = %v, want a positive duration", timing.Total)
	}
	if timing.Data < 2*time.Millisecond {
		t.Errorf("Timing.Data = %v, want at least the 2ms the handler slept", timing.Data)
	}
	if timing.Template <= 0 {
		t.Errorf("Timing.Template = %v, want a positive duration", timing.Template)
	}
	// Template time excludes the nested work that happens inside a parent's
	// template, so it must not have absorbed the child's sleep.
	if timing.Template >= timing.Data {
		t.Errorf("Timing.Template = %v, want it well under Timing.Data = %v: nested time is double counted", timing.Template, timing.Data)
	}
	if timing.Total < timing.Data {
		t.Errorf("Timing.Total = %v, want at least Timing.Data = %v", timing.Total, timing.Data)
	}
	if timing.Fragments != 2 {
		t.Errorf("Timing.Fragments = %d, want 2", timing.Fragments)
	}
	if timing.CacheHit {
		t.Error("Timing.CacheHit = true, want false: the render engine never serves from cache")
	}
	if result.Metadata.Fragments[0].Duration < result.Metadata.Fragments[1].Duration {
		t.Error("parent fragment duration is shorter than its child's, want it to include the child")
	}
}

func TestRender_ReportsMetrics(t *testing.T) {
	metrics := observability.NewRecordingMetrics()
	engine := newEngine(t, Options{Metrics: metrics})
	boom := errors.New("collage: boom")

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
	child := fragment("child", "leaf.html")
	child.DataHandler = failingHandler(boom)
	bind(t, layout, "content", child)

	if _, err := renderPage(t, engine, pageWith(layout)); err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	snapshot := metrics.Snapshot()
	if len(snapshot.RenderDurations) != 1 {
		t.Fatalf("RenderDuration calls = %d, want 1", len(snapshot.RenderDurations))
	}
	if call := snapshot.RenderDurations[0]; call.Page != "test-page" || call.CacheHit || call.Duration <= 0 {
		t.Errorf("RenderDuration call = %+v, want page test-page, no cache hit, positive duration", call)
	}

	if len(snapshot.FragmentDurations) != 2 {
		t.Fatalf("FragmentDuration calls = %d, want one per fragment", len(snapshot.FragmentDurations))
	}
	byFragment := make(map[string]observability.FragmentDurationCall, 2)
	for _, call := range snapshot.FragmentDurations {
		byFragment[call.Fragment] = call
	}
	if call, ok := byFragment["child"]; !ok || !errors.Is(call.Err, boom) {
		t.Errorf("FragmentDuration for child = %+v, want it to carry the failure", call)
	}
	if call, ok := byFragment["layout"]; !ok || call.Err != nil {
		t.Errorf("FragmentDuration for layout = %+v, want no error", call)
	}
}

// TestRender_ConcurrentRendersOfOnePage is the proof that the per-render slot
// function is genuinely per-render: every goroutine shares one engine, one page and
// one fragment tree, and each must still see its own slot bindings and identical
// output. Run under -race it also covers the template set being cloned rather than
// mutated.
func TestRender_ConcurrentRendersOfOnePage(t *testing.T) {
	engine := newEngine(t, Options{Metrics: observability.NewRecordingMetrics()})

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content", AllowMultiple: true})
	section := declare(fragment("section", "section.html"), &types.SlotDefinition{Name: "inner"})
	bind(t, section, "inner", fragment("leaf", "leaf.html"))
	bind(t, layout, "content", section, fragment("b", "b.html"))
	page := pageWith(layout, "nav")

	const want = "<html><body><section><span>leaf</span></section><i>B</i></body></html>"

	var group sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for pass := 0; pass < 25; pass++ {
				rc := types.NewRenderContext(context.Background(), nil, page, "en", nil)
				result, err := engine.Render(context.Background(), rc)
				if err != nil {
					t.Errorf("Render() error = %v", err)
					return
				}
				if string(result.HTML) != want {
					t.Errorf("Render() HTML = %q, want %q", result.HTML, want)
					return
				}
			}
		}()
	}
	group.Wait()
}

// TestRender_FailedRenderStillReportsMetadata covers the guarantee that every render
// has timing metadata, including the ones that failed: a fatal failure is exactly the
// render an operator needs to see timings and per-fragment detail for, and dropping
// them would leave the observability layer blind to it.
func TestRender_FailedRenderStillReportsMetadata(t *testing.T) {
	metrics := observability.NewRecordingMetrics()
	engine := newEngine(t, Options{Metrics: metrics})
	boom := errors.New("collage: critical data missing")

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
	child := fragment("child", "leaf.html")
	child.Required = true
	child.DataHandler = func(context.Context, *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		time.Sleep(time.Millisecond)
		return nil, []string{"post:7"}, boom
	}
	bind(t, layout, "content", child)

	result, err := renderPage(t, engine, pageWith(layout))
	if !errors.Is(err, boom) {
		t.Fatalf("Render() error = %v, want the required fragment's failure", err)
	}
	if result == nil || result.Metadata == nil {
		t.Fatalf("Render() result = %+v, want a non-nil Result carrying metadata", result)
	}
	if result.HTML != nil {
		t.Errorf("Render() HTML = %q, want nil: a failed render must not hand back markup", result.HTML)
	}

	if result.Metadata.Timing.Total <= 0 {
		t.Errorf("Timing.Total = %v, want the time the failed render consumed", result.Metadata.Timing.Total)
	}
	if result.Metadata.Page != "test-page" {
		t.Errorf("Metadata.Page = %q, want %q", result.Metadata.Page, "test-page")
	}
	if names := fragmentNames(result); !slices.Equal(names, []string{"layout", "child"}) {
		t.Errorf("Metadata.Fragments names = %v, want everything visited before the failure", names)
	}
	if meta := fragmentMetadata(t, result, "child"); !meta.Failed || !errors.Is(meta.Err, boom) {
		t.Errorf("FragmentMetadata for child = %+v, want the failure recorded", meta)
	}
	if !result.Degraded() {
		t.Error("Degraded() = false, want true for a render that failed")
	}

	snapshot := metrics.Snapshot()
	if len(snapshot.RenderDurations) != 1 {
		t.Fatalf("RenderDuration calls = %d, want 1 even though the render failed", len(snapshot.RenderDurations))
	}
	if call := snapshot.RenderDurations[0]; call.Page != "test-page" || call.Duration <= 0 {
		t.Errorf("RenderDuration call = %+v, want page test-page with a positive duration", call)
	}
}
