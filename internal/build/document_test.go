package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// newTestDocument builds a minimally valid *types.Document for these tests:
// Builder never calls Document.Validate, so only the fields Builder itself reads
// need to be set. The handler is never invoked either — fakeRenderer's
// RenderDocumentPath stands in for it — but is included so the value resembles a
// real registration.
func newTestDocument(name string, strategy types.RenderStrategy, paths map[string]string) *types.Document {
	return &types.Document{
		Name:        name,
		Strategy:    strategy,
		Paths:       paths,
		ContentType: "application/octet-stream",
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, nil
		},
	}
}

// fakeDocumentPathProvider is a DocumentPathProvider backed by a fixed table,
// keyed by document name and locale, or a fixed error. It is DocumentPathProvider's
// counterpart to fakePathProvider.
type fakeDocumentPathProvider struct {
	instances map[string][]PathInstance
	err       error
}

func (p *fakeDocumentPathProvider) Paths(_ context.Context, doc *types.Document, locale string) ([]PathInstance, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.instances[doc.Name+"|"+locale], nil
}

var _ DocumentPathProvider = (*fakeDocumentPathProvider)(nil)

// TestBuild_WritesADocumentToItsLiteralPath is the required test for the one
// behavioural difference between a page and a document: "/sitemap.xml" must
// produce "<out>/sitemap.xml" with the handler's exact bytes, and must NOT produce
// "<out>/sitemap.xml/index.html" — writing a directory where a crawler expects a
// file is the mistake this test exists to catch, so the negative is asserted
// explicitly rather than merely checking the positive.
func TestBuild_WritesADocumentToItsLiteralPath(t *testing.T) {
	out := resolvedTempDir(t)
	doc := newTestDocument("sitemap", types.StrategyStatic, map[string]string{"en": "/sitemap.xml"})
	app := &fakeRenderer{
		documents: []*types.Document{doc},
		docBodies: map[string][]byte{renderKey("/sitemap.xml", "en"): []byte("<urlset></urlset>")},
	}

	b, err := New(app, Options{OutDir: out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := filepath.Join(out, "sitemap.xml")
	got := readFile(t, want)
	if got != "<urlset></urlset>" {
		t.Fatalf("content = %q, want the handler's exact bytes", got)
	}
	if len(report.Written) != 1 || report.Written[0] != want {
		t.Fatalf("Written = %v, want [%s]", report.Written, want)
	}

	// The negative this test exists to catch.
	if _, err := os.Stat(filepath.Join(out, "sitemap.xml", "index.html")); err == nil {
		t.Fatal("sitemap.xml/index.html exists; a document must write its literal path, not a directory")
	}
}

// TestBuild_SkipsDynamicStrategyDocuments verifies a StrategyDynamic document is
// recorded in Report.Skipped with a reason and nothing is written for it, mirroring
// TestBuild_DynamicStrategySkipped for pages.
func TestBuild_SkipsDynamicStrategyDocuments(t *testing.T) {
	out := resolvedTempDir(t)
	doc := newTestDocument("live", types.StrategyDynamic, map[string]string{"en": "/live.json"})
	app := &fakeRenderer{documents: []*types.Document{doc}}

	b, err := New(app, Options{OutDir: out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(report.Skipped) != 1 || report.Skipped[0].Page != "live" || report.Skipped[0].Locale != "" {
		t.Fatalf("Skipped = %+v", report.Skipped)
	}
	if report.Skipped[0].Reason == "" {
		t.Fatal("Skipped[0].Reason is empty, want an explanation")
	}
	if len(report.Written) != 0 {
		t.Fatalf("Written = %v, want none", report.Written)
	}
	if _, err := os.Stat(filepath.Join(out, "live.json")); err == nil {
		t.Fatal("a dynamic-strategy document was written")
	}
}

// TestBuild_SkipsADynamicPatternWithoutAProvider verifies a document whose pattern
// contains a "{param}" segment, with no DocumentPathProvider configured, is
// recorded as skipped wrapping ErrDynamicPathUnresolved — the exact sentinel a
// dynamic page without a PathProvider also records, since the failure mode is
// identical — and that the build still succeeds overall.
func TestBuild_SkipsADynamicPatternWithoutAProvider(t *testing.T) {
	out := resolvedTempDir(t)
	doc := newTestDocument("api-item", types.StrategyStatic, map[string]string{"en": "/api/{id}.json"})
	app := &fakeRenderer{documents: []*types.Document{doc}}

	b, err := New(app, Options{OutDir: out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build returned an error for a skip, want nil: %v", err)
	}

	if len(report.Skipped) != 1 {
		t.Fatalf("Skipped = %v, want 1 entry", report.Skipped)
	}
	got := report.Skipped[0]
	if got.Page != "api-item" || got.Locale != "en" || got.Reason != ErrDynamicPathUnresolved.Error() {
		t.Fatalf("Skipped[0] = %+v", got)
	}
	if len(report.Written) != 0 {
		t.Fatalf("Written = %v, want none", report.Written)
	}
}

// TestBuild_UsesTheDocumentPathProvider verifies a dynamic document pattern with a
// configured DocumentPathProvider that returns two concrete ids writes both files,
// and that the provider's params reached RenderDocumentPath.
func TestBuild_UsesTheDocumentPathProvider(t *testing.T) {
	out := resolvedTempDir(t)
	doc := newTestDocument("api-item", types.StrategyStatic, map[string]string{"en": "/api/{id}.json"})
	app := &fakeRenderer{documents: []*types.Document{doc}}
	provider := &fakeDocumentPathProvider{instances: map[string][]PathInstance{
		"api-item|en": {
			{Path: "/api/1.json", Params: map[string]string{"id": "1"}},
			{Path: "/api/2.json", Params: map[string]string{"id": "2"}},
		},
	}}

	b, err := New(app, Options{OutDir: out, DocumentPathProvider: provider})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	one := filepath.Join(out, "api", "1.json")
	two := filepath.Join(out, "api", "2.json")
	if _, err := os.Stat(one); err != nil {
		t.Fatalf("stat %s: %v", one, err)
	}
	if _, err := os.Stat(two); err != nil {
		t.Fatalf("stat %s: %v", two, err)
	}
	if len(report.Written) != 2 {
		t.Fatalf("Written = %v, want 2 entries", report.Written)
	}

	app.mu.Lock()
	defer app.mu.Unlock()
	found := false
	for _, call := range app.docCalls {
		if call.path == "/api/1.json" && call.params["id"] == "1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("RenderDocumentPath was not called with the provider's params: %+v", app.docCalls)
	}
}

// TestBuild_ADocumentPathCannotEscapeOutDir is the required hostile test: a
// DocumentPathProvider is user code, and a Path resolving outside OutDir must be
// refused, with nothing written outside OutDir. It exercises documentTarget's reuse
// of resolveTarget's containment check.
func TestBuild_ADocumentPathCannotEscapeOutDir(t *testing.T) {
	out := resolvedTempDir(t)
	doc := newTestDocument("api-item", types.StrategyStatic, map[string]string{"en": "/api/{id}.json"})
	app := &fakeRenderer{documents: []*types.Document{doc}}
	provider := &fakeDocumentPathProvider{instances: map[string][]PathInstance{
		"api-item|en": {
			{Path: "/../../../etc/cron.d/evil", Params: map[string]string{"id": "../../../etc/cron.d/evil"}},
		},
	}}

	b, err := New(app, Options{OutDir: out, DocumentPathProvider: provider})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, buildErr := b.Build(context.Background())
	if buildErr == nil {
		t.Fatal("Build succeeded for an escaping document path, want an error")
	}
	if !errors.Is(buildErr, ErrPathEscapesOutDir) {
		t.Fatalf("err = %v, want ErrPathEscapesOutDir", buildErr)
	}
	if len(report.Written) != 0 {
		t.Fatalf("Written = %v, want none", report.Written)
	}

	if _, err := os.Stat(filepath.Join(out, "..", "..", "..", "etc", "cron.d", "evil")); err == nil {
		t.Fatal("escape target was written to disk")
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("OutDir has unexpected entries: %v", entries)
	}
}

// TestBuild_ARefusedDocumentDoesNotStopTheBuild is the required hostile test: a
// document whose render fails is recorded in the report, and the build continues —
// a second, healthy document still gets written.
func TestBuild_ARefusedDocumentDoesNotStopTheBuild(t *testing.T) {
	out := resolvedTempDir(t)
	broken := newTestDocument("broken", types.StrategyStatic, map[string]string{"en": "/broken.json"})
	ok := newTestDocument("ok", types.StrategyStatic, map[string]string{"en": "/ok.json"})
	renderErr := errors.New("boom")
	app := &fakeRenderer{
		documents: []*types.Document{broken, ok},
		docFail:   map[string]error{renderKey("/broken.json", "en"): renderErr},
	}

	b, err := New(app, Options{OutDir: out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, buildErr := b.Build(context.Background())
	if buildErr == nil {
		t.Fatal("Build succeeded despite a failing document, want an error")
	}
	if !errors.Is(buildErr, renderErr) {
		t.Fatalf("err = %v, want it to wrap %v", buildErr, renderErr)
	}
	if !strings.Contains(buildErr.Error(), `"broken"`) {
		t.Fatalf("err = %v, want it to name the failing document %q", buildErr, "broken")
	}

	if len(report.Errors) != 1 {
		t.Fatalf("Errors = %v, want 1 entry", report.Errors)
	}
	if len(report.Written) != 1 {
		t.Fatalf("Written = %v, want 1 entry (the document that succeeded)", report.Written)
	}
	want := filepath.Join(out, "ok.json")
	if report.Written[0] != want {
		t.Fatalf("Written[0] = %s, want %s", report.Written[0], want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("stat %s: %v", want, err)
	}
	if _, err := os.Stat(filepath.Join(out, "broken.json")); err == nil {
		t.Fatal("the failing document's file should not exist")
	}
}

// TestBuild_EmptyDocumentBodyIsNotWritten covers the fix-round-1 parity gap: a
// document handler returning "nil, nil, nil" — a successful render with no body —
// must not reach disk as a zero-byte file the way it used to. internal/httpx
// refuses to *serve* exactly this as types.ErrEmptyDocumentBody, a 500, because a
// document has no equivalent of a page's deliberate empty render; a static build
// reusing that same sentinel means the built artifact matches what the live server
// would have done, rather than silently shipping a broken sitemap or feed. It must
// be recorded, not written, and — like a document whose render fails outright — the
// build must continue: a second, healthy document still gets written.
func TestBuild_EmptyDocumentBodyIsNotWritten(t *testing.T) {
	out := resolvedTempDir(t)
	empty := newTestDocument("empty", types.StrategyStatic, map[string]string{"en": "/empty.json"})
	ok := newTestDocument("ok", types.StrategyStatic, map[string]string{"en": "/ok.json"})
	app := &fakeRenderer{
		documents: []*types.Document{empty, ok},
		// A nil entry for this key is exactly "nil, nil, nil": hasBody is true (the
		// key is present) and body is nil, so RenderDocumentPath returns a
		// successful *render.DocumentResult with a nil Body and a nil error — the
		// shape a handler that forgot to populate its body produces.
		docBodies: map[string][]byte{renderKey("/empty.json", "en"): nil},
	}

	b, err := New(app, Options{OutDir: out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, buildErr := b.Build(context.Background())

	if !errors.Is(buildErr, types.ErrEmptyDocumentBody) {
		t.Fatalf("Build error = %v, want it to wrap types.ErrEmptyDocumentBody", buildErr)
	}
	if len(report.Errors) != 1 || !errors.Is(report.Errors[0], types.ErrEmptyDocumentBody) {
		t.Fatalf("Report.Errors = %v, want one ErrEmptyDocumentBody", report.Errors)
	}
	if _, statErr := os.Stat(filepath.Join(out, "empty.json")); !os.IsNotExist(statErr) {
		t.Errorf("a zero-byte document was written: stat error = %v, want not-exist", statErr)
	}

	// The rest of the build still happens: one refused document does not withhold
	// the rest of the site.
	want := filepath.Join(out, "ok.json")
	if len(report.Written) != 1 || report.Written[0] != want {
		t.Fatalf("Report.Written = %v, want only [%s]", report.Written, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("stat %s: %v", want, err)
	}
}
