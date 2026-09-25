package render

import (
	"context"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/template"
	"github.com/Elagoht/collage/internal/types"
)

func TestRender_LayoutAndContentThroughSlot(t *testing.T) {
	engine := newEngine(t, Options{})

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
	content := fragment("content", "simple.html")
	content.DataHandler = dataHandler(map[string]string{"Title": "Home"})
	bind(t, layout, "content", content)

	result, err := renderPage(t, engine, pageWith(layout))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	want := "<html><body><h1>Home</h1></body></html>"
	if string(result.HTML) != want {
		t.Errorf("Render() HTML = %q, want %q", result.HTML, want)
	}
	if names := fragmentNames(result); !slices.Equal(names, []string{"layout", "content"}) {
		t.Errorf("Metadata.Fragments names = %v, want the parent before the child", names)
	}
}

func TestRender_NestedSlotsThreeDeep(t *testing.T) {
	engine := newEngine(t, Options{})

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
	section := declare(fragment("section", "section.html"), &types.SlotDefinition{Name: "inner"})
	bind(t, section, "inner", fragment("leaf", "leaf.html"))
	bind(t, layout, "content", section)

	result, err := renderPage(t, engine, pageWith(layout))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	want := "<html><body><section><span>leaf</span></section></body></html>"
	if string(result.HTML) != want {
		t.Errorf("Render() HTML = %q, want %q", result.HTML, want)
	}
	if got := result.Metadata.Timing.Fragments; got != 3 {
		t.Errorf("Timing.Fragments = %d, want 3", got)
	}
}

func TestRender_BindingOrderIsPreserved(t *testing.T) {
	engine := newEngine(t, Options{})

	tests := []struct {
		name  string
		first string
		want  string
	}{
		{name: "a bound first", first: "a", want: "<html><body><i>A</i><i>B</i></body></html>"},
		{name: "b bound first", first: "b", want: "<html><body><i>B</i><i>A</i></body></html>"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content", AllowMultiple: true})
			alpha, beta := fragment("a", "a.html"), fragment("b", "b.html")
			if test.first == "a" {
				bind(t, layout, "content", alpha, beta)
			} else {
				bind(t, layout, "content", beta, alpha)
			}

			result, err := renderPage(t, engine, pageWith(layout))
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if string(result.HTML) != test.want {
				t.Errorf("Render() HTML = %q, want %q", result.HTML, test.want)
			}
		})
	}
}

// TestRender_UndeclaredSlotRendersNothing: calling a slot in a template declares
// it, and a slot nothing is bound to is empty. The typo this used to catch — a fill
// on one side, a call on the other — is caught at registration instead.
func TestRender_UndeclaredSlotRendersNothing(t *testing.T) {
	engine := newEngine(t, Options{})

	content := declare(fragment("content", "unknownslot.html"), &types.SlotDefinition{Name: "declared"})
	content.Required = true

	result, err := renderPage(t, engine, pageWith(content))
	if err != nil {
		t.Fatalf("Render() error = %v, want nil", err)
	}
	if string(result.HTML) != "<div></div>" {
		t.Errorf("Render() HTML = %q, want the empty slot", result.HTML)
	}
}

func TestRender_RequiredSlotEmpty(t *testing.T) {
	engine := newEngine(t, Options{})

	tests := []struct {
		name         string
		templatePath string
	}{
		{name: "template asks for the slot", templatePath: "requiredslot.html"},
		// The check runs before the template, so a required slot is still enforced
		// when the template never renders it.
		{name: "template never asks for the slot", templatePath: "plain.html"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := declare(fragment("content", test.templatePath), &types.SlotDefinition{Name: "must", Required: true})
			content.Required = true

			_, err := renderPage(t, engine, pageWith(content))
			if !errors.Is(err, ErrRequiredSlotEmpty) {
				t.Fatalf("Render() error = %v, want ErrRequiredSlotEmpty", err)
			}
			if !strings.Contains(err.Error(), `"must"`) {
				t.Errorf("Render() error = %q, want it to name the empty slot", err)
			}
		})
	}
}

func TestRender_FragmentFailurePolicy(t *testing.T) {
	boom := errors.New("collage: data source down")
	fallbackBoom := errors.New("collage: fallback source down")

	tests := []struct {
		name string
		// configure adapts the failing child fragment.
		configure        func(child *types.Fragment)
		wantRenderErr    bool
		wantHTML         string
		wantUsedFallback bool
		wantErrs         []error
	}{
		{
			name:          "required fragment failure fails the whole render",
			configure:     func(child *types.Fragment) { child.Required = true },
			wantRenderErr: true,
			wantErrs:      []error{boom},
		},
		{
			name:      "optional fragment with no fallback emits nothing",
			configure: func(*types.Fragment) {},
			wantHTML:  "<html><body></body></html>",
			wantErrs:  []error{boom},
		},
		{
			name: "optional fragment renders its fallback",
			configure: func(child *types.Fragment) {
				child.Fallback = fragment("child-fallback", "fallback.html")
			},
			wantHTML:         "<html><body><p>fallback</p></body></html>",
			wantUsedFallback: true,
			wantErrs:         []error{boom},
		},
		{
			name: "a failing fallback never escalates to a page failure",
			configure: func(child *types.Fragment) {
				child.Fallback = fragment("child-fallback", "fallback.html")
				child.Fallback.DataHandler = failingHandler(fallbackBoom)
			},
			wantHTML: "<html><body></body></html>",
			wantErrs: []error{boom, fallbackBoom},
		},
		{
			name: "a required fallback still does not escalate",
			configure: func(child *types.Fragment) {
				child.Fallback = fragment("child-fallback", "fallback.html")
				child.Fallback.Required = true
				child.Fallback.DataHandler = failingHandler(fallbackBoom)
			},
			wantHTML: "<html><body></body></html>",
			wantErrs: []error{boom, fallbackBoom},
		},
	}

	engine := newEngine(t, Options{})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
			child := fragment("child", "leaf.html")
			child.DataHandler = failingHandler(boom)
			test.configure(child)
			bind(t, layout, "content", child)

			result, err := renderPage(t, engine, pageWith(layout))

			if test.wantRenderErr {
				if err == nil {
					t.Fatalf("Render() error = nil, want the required fragment's failure to propagate")
				}
				for _, want := range test.wantErrs {
					if !errors.Is(err, want) {
						t.Errorf("Render() error = %v, want it to wrap %v", err, want)
					}
				}
				if result == nil || result.Metadata == nil {
					t.Fatalf("Render() result = %+v, want a non-nil Result carrying metadata", result)
				}
				if result.HTML != nil {
					t.Errorf("Render() HTML = %q, want nil alongside an error", result.HTML)
				}
				return
			}

			if err != nil {
				t.Fatalf("Render() error = %v, want the failure to be contained", err)
			}
			if string(result.HTML) != test.wantHTML {
				t.Errorf("Render() HTML = %q, want %q", result.HTML, test.wantHTML)
			}
			if !result.Degraded() {
				t.Error("Degraded() = false, want true after a fragment failed")
			}

			meta := fragmentMetadata(t, result, "child")
			if !meta.Failed {
				t.Error("FragmentMetadata.Failed = false, want true")
			}
			if meta.UsedFallback != test.wantUsedFallback {
				t.Errorf("FragmentMetadata.UsedFallback = %v, want %v", meta.UsedFallback, test.wantUsedFallback)
			}
			if meta.Err == nil {
				t.Fatal("FragmentMetadata.Err = nil, want the failure to be recorded")
			}
			for _, want := range test.wantErrs {
				if !errors.Is(meta.Err, want) {
					t.Errorf("FragmentMetadata.Err = %v, want it to wrap %v", meta.Err, want)
				}
			}
		})
	}
}

// TestRender_NilSlotDefinitionIsAnError covers a slot map entry mapped to nil.
// Fragment.Slot reports that the slot exists, and Render never insists that
// Fragment.Validate has run, so without an explicit check the fill loop dereferences
// nil — and a contained panic would make "invalid memory address" the page's recorded
// failure reason instead of the actual fault.
func TestRender_NilSlotDefinitionIsAnError(t *testing.T) {
	engine := newEngine(t, Options{})

	content := fragment("content", "layout.html")
	content.Slots = map[string]*types.SlotDefinition{"content": nil}
	content.Required = true

	_, err := renderPage(t, engine, pageWith(content))
	if !errors.Is(err, types.ErrInvalidSlotDefinition) {
		t.Fatalf("Render() error = %v, want types.ErrInvalidSlotDefinition", err)
	}
	var panicErr *PanicError
	if errors.As(err, &panicErr) {
		t.Errorf("Render() error = %v, want a described fault rather than a recovered nil dereference", err)
	}
	if !strings.Contains(err.Error(), `"content"`) {
		t.Errorf("Render() error = %q, want it to name the malformed slot", err)
	}
}

// TestRender_RequiredInsideAFallbackDoesNotEscalate pins which of two overlapping
// rules wins. Required-ness is scoped to the primary tree: "the page cannot render
// without me" and "this alternative cannot render without me" are different claims,
// and escalating the second would let a broken fallback take down the page it exists
// to protect. So the page renders, degraded, without the fragment.
func TestRender_RequiredInsideAFallbackDoesNotEscalate(t *testing.T) {
	engine := newEngine(t, Options{})
	boom := errors.New("collage: primary source down")
	grandchildBoom := errors.New("collage: fallback source down")

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})

	child := fragment("child", "leaf.html")
	child.DataHandler = failingHandler(boom)
	child.Fallback = declare(fragment("child-fallback", "section.html"), &types.SlotDefinition{Name: "inner"})

	grandchild := fragment("fallback-grandchild", "leaf.html")
	grandchild.Required = true
	grandchild.DataHandler = failingHandler(grandchildBoom)
	bind(t, child.Fallback, "inner", grandchild)
	bind(t, layout, "content", child)

	result, err := renderPage(t, engine, pageWith(layout))
	if err != nil {
		t.Fatalf("Render() error = %v, want a required fragment inside a fallback not to fail the page", err)
	}
	if string(result.HTML) != "<html><body></body></html>" {
		t.Errorf("Render() HTML = %q, want the page without the fragment", result.HTML)
	}
	if !result.Degraded() {
		t.Error("Degraded() = false, want true: the page is missing a fragment")
	}

	meta := fragmentMetadata(t, result, "child")
	for _, want := range []error{boom, grandchildBoom} {
		if !errors.Is(meta.Err, want) {
			t.Errorf("FragmentMetadata.Err = %v, want it to wrap %v", meta.Err, want)
		}
	}
	if meta.UsedFallback {
		t.Error("FragmentMetadata.UsedFallback = true, want false: the fallback did not produce output")
	}
	if grandchildMeta := fragmentMetadata(t, result, "fallback-grandchild"); !grandchildMeta.Failed {
		t.Error("the required fragment inside the fallback was not recorded as failed")
	}
}

// TestRender_RequiredFragmentSkipsItsOwnFallback pins the precedence between Required
// and Fallback on one and the same fragment: Required wins, and the fallback is never
// even attempted. Rendering it would produce output for a page that is about to fail.
func TestRender_RequiredFragmentSkipsItsOwnFallback(t *testing.T) {
	engine := newEngine(t, Options{})
	boom := errors.New("collage: primary source down")

	fallbackRan := false
	child := fragment("child", "leaf.html")
	child.Required = true
	child.DataHandler = failingHandler(boom)
	child.Fallback = fragment("child-fallback", "fallback.html")
	child.Fallback.DataHandler = func(context.Context, *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		fallbackRan = true
		return nil, nil, nil
	}

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
	bind(t, layout, "content", child)

	if _, err := renderPage(t, engine, pageWith(layout)); !errors.Is(err, boom) {
		t.Fatalf("Render() error = %v, want the required fragment's failure to propagate", err)
	}
	if fallbackRan {
		t.Error("the fallback of a required fragment was rendered, want it skipped entirely")
	}
}

// TestRender_RequiredChildIsNotAbsorbedByAnAncestorFallback covers the case the
// failure policy is silent about: a required fragment nested under an optional
// ancestor that has a fallback. "Required" has to mean the page fails, or the
// ancestor's fallback would quietly paper over exactly the failure the fragment was
// marked required to prevent.
func TestRender_RequiredChildIsNotAbsorbedByAnAncestorFallback(t *testing.T) {
	engine := newEngine(t, Options{})
	boom := errors.New("collage: critical data missing")

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
	layout.Fallback = fragment("layout-fallback", "fallback.html")
	child := fragment("child", "leaf.html")
	child.Required = true
	child.DataHandler = failingHandler(boom)
	bind(t, layout, "content", child)

	result, err := renderPage(t, engine, pageWith(layout))
	if !errors.Is(err, boom) {
		t.Fatalf("Render() error = %v, want the required child's failure to reach the caller", err)
	}
	if result.HTML != nil {
		t.Errorf("Render() HTML = %q, want nil alongside an error", result.HTML)
	}
}

func TestRender_FailedFragmentStillContributesItsTags(t *testing.T) {
	engine := newEngine(t, Options{})

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
	child := fragment("child", "leaf.html")
	child.DataHandler = failingHandler(errors.New("collage: boom"), "post:7")
	bind(t, layout, "content", child)

	result, err := renderPage(t, engine, pageWith(layout))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !slices.Equal(result.DependencyTags, []string{"post:7"}) {
		t.Errorf("DependencyTags = %v, want the tags the handler resolved before failing", result.DependencyTags)
	}
}

// renderFailingChild renders a layout wrapping one failing fragment and returns the
// page's HTML, so the DevMode tests differ only in the inputs that matter.
func renderFailingChild(t *testing.T, devMode bool, childName string, childErr error) string {
	t.Helper()
	engine := newEngine(t, Options{DevMode: devMode})

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
	child := fragment(childName, "leaf.html")
	child.DataHandler = failingHandler(childErr)
	bind(t, layout, "content", child)

	result, err := renderPage(t, engine, pageWith(layout))
	if err != nil {
		t.Fatalf("Render() error = %v, want the failure to be contained", err)
	}
	return string(result.HTML)
}

func TestRender_DevModeShowsFailuresInTheOutput(t *testing.T) {
	t.Run("the comment names the fragment and its error", func(t *testing.T) {
		html := renderFailingChild(t, true, "child", errors.New("collage: upstream refused"))
		if !strings.Contains(html, "<!-- collage: fragment child failed:") {
			t.Errorf("Render() HTML = %q, want a dev-mode comment naming the failed fragment", html)
		}
		if !strings.Contains(html, "upstream refused") {
			t.Errorf("Render() HTML = %q, want the error text in the comment", html)
		}
	})

	t.Run("an error text cannot close the comment early", func(t *testing.T) {
		html := renderFailingChild(t, true, "child", errors.New("collage: <script>--></script>"))
		if !strings.Contains(html, "&lt;script&gt;--&gt;&lt;/script&gt;") {
			t.Errorf("Render() HTML = %q, want the error text HTML-escaped", html)
		}
		// Escaping is what keeps the error inside the comment: a comment can only
		// end at a ">", and there are none left. The only terminator in the page is
		// the one devComment wrote itself.
		if got := strings.Count(html, "-->"); got != 1 {
			t.Errorf("Render() HTML = %q, want exactly one comment terminator, got %d", html, got)
		}
	})

	t.Run("a fragment name cannot close the comment early", func(t *testing.T) {
		html := renderFailingChild(t, true, `evil--><script>alert(1)</script>`, errors.New("collage: boom"))
		if !strings.Contains(html, "evil--&gt;&lt;script&gt;") {
			t.Errorf("Render() HTML = %q, want the fragment name HTML-escaped", html)
		}
		if strings.Contains(html, "<script>") {
			t.Errorf("Render() HTML = %q, want no live markup to have escaped the comment", html)
		}
		if got := strings.Count(html, "-->"); got != 1 {
			t.Errorf("Render() HTML = %q, want exactly one comment terminator, got %d", html, got)
		}
	})
}

// TestRender_WithoutDevModeFailuresAreSilent is the other half of the DevMode
// contract: outside dev mode a failed fragment leaks nothing into the page. Its name
// and its error can both carry internal detail, and a production page must not
// whisper either of them to a visitor.
func TestRender_WithoutDevModeFailuresAreSilent(t *testing.T) {
	const (
		name     = "internal-pricing-service"
		errText  = "collage: dsn=postgres://user:hunter2@db.internal refused"
		wantHTML = "<html><body></body></html>"
	)

	html := renderFailingChild(t, false, name, errors.New(errText))

	if html != wantHTML {
		t.Errorf("Render() HTML = %q, want exactly %q", html, wantHTML)
	}
	for _, leak := range []string{name, errText, "hunter2", "<!--", "-->", "collage:"} {
		if strings.Contains(html, leak) {
			t.Errorf("Render() HTML = %q, want no trace of %q outside dev mode", html, leak)
		}
	}
}

func TestRender_DataHandlerPanicIsContained(t *testing.T) {
	engine := newEngine(t, Options{})

	content := fragment("content", "leaf.html")
	content.Required = true
	content.DataHandler = func(context.Context, *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		panic("handler boom")
	}

	_, err := renderPage(t, engine, pageWith(content))

	var panicErr *PanicError
	if !errors.As(err, &panicErr) {
		t.Fatalf("Render() error = %v, want a *PanicError", err)
	}
	if panicErr.Value != "handler boom" {
		t.Errorf("PanicError.Value = %v, want %q", panicErr.Value, "handler boom")
	}
	if !strings.Contains(err.Error(), "content") {
		t.Errorf("Render() error = %q, want it to name the fragment that panicked", err)
	}
}

// TestRender_TemplateFunctionPanicIsContained uses an engine whose "upper" panics.
// text/template recovers a panic raised inside a called function itself, so this
// asserts the contract callers care about — the panic becomes this fragment's error
// and the process survives — rather than the mechanism that caught it.
func TestRender_TemplateFunctionPanicIsContained(t *testing.T) {
	tmpl, err := template.NewHTML(template.HTMLConfig{
		Root:      "testdata",
		Extension: ".html",
		Funcs: htmltemplate.FuncMap{
			"upper": func(string) string { panic("template boom") },
		},
	})
	if err != nil {
		t.Fatalf("template.NewHTML() error = %v", err)
	}
	engine := New(tmpl, Options{})

	layout := declare(fragment("layout", "layout.html"), &types.SlotDefinition{Name: "content"})
	child := fragment("child", "funcpanic.html")
	bind(t, layout, "content", child)

	result, renderErr := renderPage(t, engine, pageWith(layout))
	if renderErr != nil {
		t.Fatalf("Render() error = %v, want the panic contained as a fragment failure", renderErr)
	}
	if string(result.HTML) != "<html><body></body></html>" {
		t.Errorf("Render() HTML = %q, want the failed fragment to emit nothing", result.HTML)
	}
	meta := fragmentMetadata(t, result, "child")
	if meta.Err == nil || !strings.Contains(meta.Err.Error(), "template boom") {
		t.Errorf("FragmentMetadata.Err = %v, want it to carry the panic value", meta.Err)
	}
}

func TestRender_DataHandlerTimeout(t *testing.T) {
	tests := []struct {
		name            string
		fragmentTimeout time.Duration
		defaultTimeout  time.Duration
	}{
		{name: "fragment timeout", fragmentTimeout: 20 * time.Millisecond, defaultTimeout: time.Hour},
		{name: "engine default timeout", fragmentTimeout: 0, defaultTimeout: 20 * time.Millisecond},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine := newEngine(t, Options{DefaultTimeout: test.defaultTimeout})

			content := fragment("content", "leaf.html")
			content.Required = true
			content.Timeout = test.fragmentTimeout
			content.DataHandler = func(ctx context.Context, _ *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
				select {
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				case <-time.After(5 * time.Second):
					return nil, nil, nil
				}
			}

			start := time.Now()
			_, err := renderPage(t, engine, pageWith(content))
			elapsed := time.Since(start)

			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Render() error = %v, want context.DeadlineExceeded", err)
			}
			if elapsed > time.Second {
				t.Errorf("Render() took %s, want it to stop near the 20ms deadline", elapsed)
			}
		})
	}
}

func TestRender_MaxDepth(t *testing.T) {
	tests := []struct {
		name     string
		maxDepth int
		depth    int
		wantErr  bool
	}{
		{name: "exactly at the limit", maxDepth: 3, depth: 3},
		{name: "one past the limit", maxDepth: 3, depth: 4, wantErr: true},
		{name: "default limit accommodates 32", depth: 32},
		{name: "default limit trips at 33", depth: 33, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics := observability.NewRecordingMetrics()
			engine := newEngine(t, Options{MaxDepth: test.maxDepth, Metrics: metrics})
			root := chainOf(t, test.depth)

			result, err := renderPage(t, engine, pageWith(root))
			if !test.wantErr {
				if err != nil {
					t.Fatalf("Render() error = %v, want a depth of %d to be allowed", err, test.depth)
				}
				if got := result.Metadata.Timing.Fragments; got != test.depth {
					t.Errorf("Timing.Fragments = %d, want %d", got, test.depth)
				}
				return
			}

			if !errors.Is(err, ErrMaxDepthExceeded) {
				t.Fatalf("Render() error = %v, want ErrMaxDepthExceeded", err)
			}
			// The frame that trips the limit is rejected before it runs, so it gets
			// neither a metadata entry nor a duration metric — only the error names
			// it. Metadata covers the fragments that actually executed.
			if got := len(result.Metadata.Fragments); got != test.depth-1 {
				t.Errorf("Metadata.Fragments = %d entries, want %d: one per fragment that ran", got, test.depth-1)
			}
			if got := len(metrics.Snapshot().FragmentDurations); got != test.depth-1 {
				t.Errorf("FragmentDuration calls = %d, want %d: one per fragment that ran", got, test.depth-1)
			}
			tripped := fmt.Sprintf("chain-%d", test.depth-1)
			if slices.Contains(fragmentNames(result), tripped) {
				t.Errorf("Metadata.Fragments names = %v, want no entry for %q, which never ran", fragmentNames(result), tripped)
			}
			// The chain is the only thing that makes the error actionable: it names
			// the path that ran away, which is where the accidental recursion is.
			levels := make([]string, 0, test.depth)
			for level := 0; level < test.depth; level++ {
				levels = append(levels, fmt.Sprintf("chain-%d", level))
			}
			wantChain := strings.Join(levels, " > ")
			if !strings.Contains(err.Error(), wantChain) {
				t.Errorf("Render() error = %q, want it to name the fragment chain %q", err, wantChain)
			}
		})
	}
}

// chainOf builds a chain of depth fragments, each binding the next into its "next"
// slot, so the tree nests exactly depth levels deep.
func chainOf(t *testing.T, depth int) *types.Fragment {
	t.Helper()
	root := declare(fragment("chain-0", "chain.html"), &types.SlotDefinition{Name: "next"})
	current := root
	for level := 1; level < depth; level++ {
		next := declare(fragment(fmt.Sprintf("chain-%d", level), "chain.html"), &types.SlotDefinition{Name: "next"})
		bind(t, current, "next", next)
		current = next
	}
	return root
}

// fragmentMetadata returns the metadata recorded for the named fragment.
func fragmentMetadata(t *testing.T, result *Result, name string) FragmentMetadata {
	t.Helper()
	for _, meta := range result.Metadata.Fragments {
		if meta.Name == name {
			return meta
		}
	}
	t.Fatalf("no metadata recorded for fragment %q, got %v", name, fragmentNames(result))
	return FragmentMetadata{}
}

// fragmentNames lists the fragment names in the order they were recorded.
func fragmentNames(result *Result) []string {
	names := make([]string, 0, len(result.Metadata.Fragments))
	for _, meta := range result.Metadata.Fragments {
		names = append(names, meta.Name)
	}
	return names
}
