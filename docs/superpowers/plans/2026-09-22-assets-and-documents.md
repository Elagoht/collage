# Assets and Documents Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let collage serve generated non-HTML responses (sitemap.xml, rss.xml, robots.txt, llms.txt, jwks.json) and static files (audio, zip, CSS, JS) without abandoning its cache-first machinery.

**Architecture:** Two mechanisms, deliberately separate. `Document` is a routed, cacheable, tagged payload whose handler returns bytes — it reuses the page router, cache key, ETag, tag invalidation and strategies unchanged. `Assets` mounts an `fs.FS` at a path prefix and serves it with `http.ServeContent`, giving Range requests for free and never touching the page cache.

**Tech Stack:** Go 1.26, standard library only. `io/fs`, `net/http`, `mime`, `os.Root`.

**Spec:** `docs/superpowers/specs/2026-09-22-assets-and-documents-design.md`

## Global Constraints

These bind every task. A violation is a review defect regardless of what a task says.

1. **Module path is `github.com/Elagoht/collage`.** Go 1.26.
2. **Standard library only.** No third-party dependencies; `go.sum` stays empty.
3. **No `any` / `interface{}`** unless the declaring line carries an `// any: <reason>` comment justifying it against a genuine external constraint. The justified sites today are template data, `Page.SEO`, `RenderContext.SharedData`, `PanicError.Value` and `dict`. This plan adds **zero** new ones.
4. **No reflection.**
5. **Every exported identifier has a doc comment starting with its own name.**
6. **Errors** are `var ErrFoo = errors.New("collage: foo")`, lowercase, `collage: ` prefixed, wrapped with `%w`. **One sentinel per distinct failure mode, not per field** — two checks share a sentinel only when a caller would handle them identically, and the doc comment then says the sharing is deliberate.
7. **Every sentinel an application can receive is re-exported through `pkg/collage`.** `internal/` is unreachable from other modules, so an unmatchable error is a reachability bug. The final review of the previous plan found four such gaps; do not add a fifth.
8. **Tests use the standard `testing` package** and assert real behaviour, table-driven where cases are homogeneous. A test that would pass against the pre-change code verifies nothing.
9. **`internal/*` never imports `pkg/collage`.**
10. **Verification gate**, run in the repo root before reporting a task done: `gofmt -l .` (silent), `go build ./...`, `go vet ./...`, `go test ./... -count=1`, and `-race` on every package the task touched.
11. **Never cache an error response.** 404 and 500 carry `Cache-Control: no-store`.

## File Structure

| Path | Responsibility |
|---|---|
| `internal/types/document.go` | The `Document` domain type, its handler signature, validation |
| `internal/router/document.go` | Registering documents into the shared radix tree |
| `internal/render/document.go` | Executing a document handler with timeout, panic safety, tag collection |
| `internal/asset/mount.go` | `Mount`, prefix matching, `fs.FS` serving via `http.ServeContent` |
| `internal/asset/etag.go` | Lazy, memoised content-hash ETags |
| `internal/httpx/document.go` | The document branch of the request lifecycle |
| `internal/httpx/plaintext.go` | Plain-text error responses for documents and assets |
| `internal/core/document.go` | `RegisterDocument`, `Documents`, `RenderDocumentPath`, close-out checks |
| `internal/core/mount.go` | `Mount`, prefix validation, shadow detection |
| `internal/build/document.go` | Building documents to literal paths; `DocumentPathProvider` |
| `internal/build/asset.go` | Copying mounted assets into `OutDir` |
| `pkg/collage/document.go` | `Document` aliases and `DocumentBuilder` |
| `pkg/collage/mount.go` | Mount options and re-exported mount sentinels |

---

## Task 1: The `Document` domain type

**Files:**
- Create: `internal/types/document.go`
- Modify: `internal/types/errors.go`
- Test: `internal/types/document_test.go`

**Interfaces:**
- Consumes: `RenderStrategy`, `Redirect`, `RenderContext`, and the existing sentinels `ErrEmptyName`, `ErrInvalidPath`, `ErrInvalidTTL`, `ErrMissingTTL` from `internal/types`.
- Produces: `types.Document`, `types.DocumentHandlerFunc`, `ErrNilDocument`, `ErrEmptyContentType`, `ErrNoDocumentHandler`. Tasks 2, 3, 4, 5 and 8 depend on these exact names.

- [ ] **Step 1: Write the failing test**

```go
// internal/types/document_test.go
package types

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func validDocument() *Document {
	return &Document{
		Name:        "sitemap",
		ContentType: "application/xml",
		Paths:       map[string]string{"en": "/sitemap.xml"},
		Handler: func(ctx context.Context, rc *RenderContext) ([]byte, []string, error) {
			return []byte("<urlset/>"), nil, nil
		},
	}
}

func TestDocument_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Document)
		wantErr error
	}{
		{"valid", func(*Document) {}, nil},
		{"empty name", func(d *Document) { d.Name = "" }, ErrEmptyName},
		{"empty content type", func(d *Document) { d.ContentType = "" }, ErrEmptyContentType},
		{"blank content type", func(d *Document) { d.ContentType = "   " }, ErrEmptyContentType},
		{"nil handler", func(d *Document) { d.Handler = nil }, ErrNoDocumentHandler},
		{"path without leading slash", func(d *Document) { d.Paths = map[string]string{"en": "sitemap.xml"} }, ErrInvalidPath},
		{"negative ttl", func(d *Document) { d.CacheTTL = -time.Second }, ErrInvalidTTL},
		{"incremental without ttl", func(d *Document) { d.Strategy = StrategyIncremental }, ErrMissingTTL},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc := validDocument()
			test.mutate(doc)

			err := doc.Validate()
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestDocument_LocalesAreSorted(t *testing.T) {
	doc := validDocument()
	doc.Paths = map[string]string{"tr": "/site-haritasi.xml", "en": "/sitemap.xml", "de": "/sitemap.xml"}

	if got, want := doc.Locales(), []string{"de", "en", "tr"}; !slices.Equal(got, want) {
		t.Fatalf("Locales() = %v, want %v", got, want)
	}
}

func TestDocument_NilReceiverIsSafe(t *testing.T) {
	var doc *Document

	if err := doc.Validate(); !errors.Is(err, ErrNilDocument) {
		t.Fatalf("Validate() = %v, want ErrNilDocument", err)
	}
	if got := doc.Locales(); got != nil {
		t.Fatalf("Locales() = %v, want nil", got)
	}
	if _, ok := doc.PathFor("en"); ok {
		t.Fatal("PathFor() ok = true, want false")
	}
}
```

- [ ] **Step 2: Run the test and confirm it fails**

Run: `go test ./internal/types/ -run TestDocument -v`
Expected: compile failure, `undefined: Document`.

- [ ] **Step 3: Add the three sentinels**

Append to `internal/types/errors.go`, following the file's existing style:

```go
// ErrNilDocument reports that a nil document was supplied where one was required.
var ErrNilDocument = errors.New("collage: nil document")

// ErrEmptyContentType reports that a document declared no content type. A
// document's content type is static and required: the framework writes it on every
// response and never guesses it.
var ErrEmptyContentType = errors.New("collage: empty content type")

// ErrNoDocumentHandler reports that a document declared no handler. Unlike a page,
// a document has no template to fall back on, so a handler is mandatory.
var ErrNoDocumentHandler = errors.New("collage: document has no handler")
```

- [ ] **Step 4: Write `internal/types/document.go`**

```go
package types

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// DocumentHandlerFunc produces a document's body. It returns the bytes to serve,
// the dependency tags the body was derived from, and an error. Returning an error
// that wraps ErrNotFound makes the response a 404 rather than a 500.
type DocumentHandlerFunc func(ctx context.Context, rc *RenderContext) (body []byte, tags []string, err error)

// Document is a routed, cacheable response that is not HTML: a sitemap, a feed, a
// robots.txt, a JWKS. It reuses a page's routing, caching, ETag and dependency-tag
// machinery, but renders no templates and composes no fragments — its handler
// returns bytes directly.
type Document struct {
	// Name identifies the document and appears in registration errors.
	Name string
	// Paths maps a locale to the URL pattern that reaches this document.
	Paths map[string]string
	// ContentType is written verbatim as the response's Content-Type. It is static
	// by design: the framework reads it from the matched document at serve time, so
	// it never has to be stored alongside the cached body.
	ContentType string
	// Handler produces the body. It is required.
	Handler DocumentHandlerFunc
	// Strategy selects how the response is cached.
	Strategy RenderStrategy
	// CacheTTL is the lifetime of a cached body under StrategyIncremental.
	CacheTTL time.Duration
	// DependencyTags are tags every response from this document carries, in
	// addition to whatever its handler returns.
	DependencyTags []string
	// Redirects are source patterns that redirect to this document.
	Redirects []*Redirect
}

// Locales returns the locales this document declares a path for, sorted.
func (d *Document) Locales() []string {
	if d == nil {
		return nil
	}
	locales := make([]string, 0, len(d.Paths))
	for locale := range d.Paths {
		locales = append(locales, locale)
	}
	slices.Sort(locales)
	return locales
}

// PathFor returns the URL pattern this document is reachable at for locale.
func (d *Document) PathFor(locale string) (string, bool) {
	if d == nil {
		return "", false
	}
	pattern, ok := d.Paths[locale]
	return pattern, ok
}

// Validate reports whether the document is coherent enough to register. It returns
// ErrNilDocument, ErrEmptyName, ErrEmptyContentType, ErrNoDocumentHandler,
// ErrInvalidPath, ErrInvalidTTL, ErrMissingTTL, or a redirect's own error.
func (d *Document) Validate() error {
	if d == nil {
		return ErrNilDocument
	}
	if d.Name == "" {
		return fmt.Errorf("%w: document", ErrEmptyName)
	}
	if strings.TrimSpace(d.ContentType) == "" {
		return fmt.Errorf("%w: document %q", ErrEmptyContentType, d.Name)
	}
	if d.Handler == nil {
		return fmt.Errorf("%w: document %q", ErrNoDocumentHandler, d.Name)
	}
	for _, locale := range d.Locales() {
		if pattern := d.Paths[locale]; !strings.HasPrefix(pattern, "/") {
			return fmt.Errorf("%w: document %q locale %q pattern %q", ErrInvalidPath, d.Name, locale, pattern)
		}
	}
	if d.CacheTTL < 0 {
		return fmt.Errorf("%w: document %q", ErrInvalidTTL, d.Name)
	}
	if d.Strategy == StrategyIncremental && d.CacheTTL <= 0 {
		return fmt.Errorf("%w: document %q", ErrMissingTTL, d.Name)
	}
	for _, redirect := range d.Redirects {
		if err := redirect.Validate(); err != nil {
			return fmt.Errorf("collage: document %q: %w", d.Name, err)
		}
	}
	return nil
}
```

Note the `Locales()` loop in `Validate`: iterating the sorted slice rather than the map keeps the error deterministic when two locales are both malformed.

- [ ] **Step 5: Run the tests and confirm they pass**

Run: `go test ./internal/types/ -run TestDocument -v -count=1`
Expected: PASS, every subtest.

- [ ] **Step 6: Run the full gate and commit**

```bash
gofmt -l . && go build ./... && go vet ./... && go test ./... -count=1
git add internal/types/document.go internal/types/document_test.go internal/types/errors.go
git commit -m "feat(types): add the Document domain type"
```

---

## Task 2: Routing documents

**Files:**
- Create: `internal/router/document.go`, `internal/router/document_test.go`
- Modify: `internal/router/router.go` (`MatchResult`, the `Router` interface, the terminal-node match), `internal/router/radix.go` (the node's second occupant field)

**Interfaces:**
- Consumes: `types.Document` from Task 1; the existing `parsePattern`, `node.insert`, `MatchResult`, `ErrDuplicateRoute`.
- Produces: `MatchResult.Document *types.Document`; `Router.RegisterDocument(*types.Document) error`. Tasks 4, 5 and 8 depend on both.

Documents share the page radix tree. That is the whole point: a collision between `/sitemap.xml` and `/{slug}` must be caught at startup, which two separate trees cannot do.

**Before writing anything, read `internal/router/router_test.go`** for the existing `newTestRouter` and `testPage` helpers and reuse them. Do not define your own — if their signatures differ from the calls below, adapt the calls.

- [ ] **Step 1: Write the failing test**

```go
// internal/router/document_test.go
package router

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

func testDocument(name, pattern string) *types.Document {
	return &types.Document{
		Name:        name,
		ContentType: "application/xml",
		Paths:       map[string]string{"en": pattern},
	}
}

func TestRegisterDocument_MatchReturnsTheDocument(t *testing.T) {
	rt := newTestRouter(t)
	doc := testDocument("sitemap", "/sitemap.xml")
	if err := rt.RegisterDocument(doc); err != nil {
		t.Fatalf("RegisterDocument() = %v", err)
	}

	match, err := rt.Match(httptest.NewRequest("GET", "/sitemap.xml", nil))
	if err != nil {
		t.Fatalf("Match() = %v", err)
	}
	if match.Document != doc {
		t.Fatalf("Document = %v, want the registered document", match.Document)
	}
	if match.Page != nil {
		t.Fatalf("Page = %v, want nil — exactly one of Page and Document is set", match.Page)
	}
	if match.IsNotFound {
		t.Fatal("IsNotFound = true, want false")
	}
}

func TestRegisterDocument_CollidesWithAPage(t *testing.T) {
	rt := newTestRouter(t)
	if err := rt.Register(testPage("post", "/sitemap.xml")); err != nil {
		t.Fatalf("Register() = %v", err)
	}

	err := rt.RegisterDocument(testDocument("sitemap", "/sitemap.xml"))
	if !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("RegisterDocument() = %v, want ErrDuplicateRoute", err)
	}
	if !strings.Contains(err.Error(), "post") || !strings.Contains(err.Error(), "sitemap") {
		t.Fatalf("error %q should name both colliding routes", err)
	}
}

func TestRegisterPage_CollidesWithADocument(t *testing.T) {
	rt := newTestRouter(t)
	if err := rt.RegisterDocument(testDocument("sitemap", "/sitemap.xml")); err != nil {
		t.Fatalf("RegisterDocument() = %v", err)
	}

	err := rt.Register(testPage("post", "/sitemap.xml"))
	if !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("Register() = %v, want ErrDuplicateRoute — the collision check must be symmetric", err)
	}
}

func TestRegisterDocument_StaticBeatsADynamicPage(t *testing.T) {
	rt := newTestRouter(t)
	if err := rt.Register(testPage("post", "/{slug}")); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	if err := rt.RegisterDocument(testDocument("robots", "/robots.txt")); err != nil {
		t.Fatalf("RegisterDocument() = %v", err)
	}

	match, err := rt.Match(httptest.NewRequest("GET", "/robots.txt", nil))
	if err != nil {
		t.Fatalf("Match() = %v", err)
	}
	if match.Document == nil || match.Document.Name != "robots" {
		t.Fatalf("Document = %v, want robots — a static document must beat a dynamic page", match.Document)
	}
	if match.Page != nil {
		t.Fatalf("Page = %v, want nil", match.Page)
	}
}

func TestRegisterDocument_LocalePrefixedPathsResolve(t *testing.T) {
	rt := newTestRouter(t)
	doc := &types.Document{
		Name:        "sitemap",
		ContentType: "application/xml",
		Paths:       map[string]string{"en": "/sitemap.xml", "tr": "/site-haritasi.xml"},
	}
	if err := rt.RegisterDocument(doc); err != nil {
		t.Fatalf("RegisterDocument() = %v", err)
	}

	match, err := rt.Match(httptest.NewRequest("GET", "/tr/site-haritasi.xml", nil))
	if err != nil {
		t.Fatalf("Match() = %v", err)
	}
	if match.Document != doc {
		t.Fatalf("Document = %v, want the document", match.Document)
	}
	if match.Locale != "tr" {
		t.Fatalf("Locale = %q, want %q", match.Locale, "tr")
	}
}
```

The locale test requires `newTestRouter` to support `tr`; read its setup and, if it only supports `en`, construct a router with `{"en", "tr"}` supported locales inline for that one test rather than changing the shared helper.

- [ ] **Step 2: Run the test and confirm it fails**

Run: `go test ./internal/router/ -run TestRegisterDocument -v`
Expected: compile failure, `rt.RegisterDocument undefined`.

- [ ] **Step 3: Widen `MatchResult`, the interface, and the node**

In `internal/router/router.go`, add to `MatchResult` immediately after `Page`:

```go
	// Document is the matched document. It is nil when a page matched, when
	// RedirectTo is set, or when IsNotFound is true. Exactly one of Page and
	// Document is non-nil on a successful match.
	Document *types.Document
```

Add to the `Router` interface:

```go
	// RegisterDocument adds a document's paths and redirects to the router. It
	// returns ErrDuplicateRoute when any of them collides with an already-registered
	// page or document.
	RegisterDocument(doc *types.Document) error
```

In `internal/router/radix.go`, beside the node's existing `page` field:

```go
	// document is the document terminating at this node, if any. At most one of
	// page and document is ever set; occupantName is the only place both are read.
	document *types.Document
```

- [ ] **Step 4: Implement `internal/router/document.go`**

```go
package router

import (
	"fmt"

	"github.com/Elagoht/collage/internal/types"
)

// RegisterDocument implements Router.
func (rt *router) RegisterDocument(doc *types.Document) error {
	for _, locale := range doc.Locales() {
		pattern := doc.Paths[locale]

		segments, err := parsePattern(pattern)
		if err != nil {
			return fmt.Errorf("collage: document %q: %w", doc.Name, err)
		}

		target, err := rt.tree.insert(segments)
		if err != nil {
			return fmt.Errorf("collage: document %q: %w", doc.Name, err)
		}
		if occupant := occupantName(target); occupant != "" {
			return fmt.Errorf("%w: document %q and %s both claim %q for locale %q",
				ErrDuplicateRoute, doc.Name, occupant, pattern, locale)
		}
		target.document = doc
	}

	return rt.registerRedirects(doc.Name, doc.Redirects)
}

// occupantName describes whatever already terminates at n, or returns the empty
// string when nothing does. It is the single place the duplicate check reads both
// occupant fields, so the page and document registration paths cannot drift.
func occupantName(n *node) string {
	switch {
	case n.page != nil:
		return fmt.Sprintf("page %q", n.page.Name)
	case n.document != nil:
		return fmt.Sprintf("document %q", n.document.Name)
	default:
		return ""
	}
}
```

Then make three edits to `internal/router/router.go`:

1. Replace `Register`'s existing duplicate check with a call to `occupantName`, so a page colliding with a document is caught symmetrically.
2. Extract the redirect-registration loop currently inside `Register` into a shared method, and call it from both:

```go
// registerRedirects adds owner's redirects to the redirect tree. owner is used
// only to name the route in errors.
func (rt *router) registerRedirects(owner string, redirects []*types.Redirect) error {
	// body moved verbatim from Register, with the page's name replaced by owner
}
```

3. Where the terminal node currently populates `MatchResult.Page`, populate `Document` from `target.document` as well.

- [ ] **Step 5: Run the tests and confirm they pass**

Run: `go test ./internal/router/ -v -count=1 && go test ./internal/router/ -race -count=1`
Expected: PASS — including every pre-existing router test. The shared-tree change must not alter page routing at all.

- [ ] **Step 6: Run the full gate and commit**

```bash
gofmt -l . && go build ./... && go vet ./... && go test ./... -count=1
git add internal/router/
git commit -m "feat(router): route documents through the shared radix tree"
```

---

## Task 3: Executing a document handler

**Files:**
- Create: `internal/render/document.go`, `internal/render/document_test.go`
- Modify: `internal/render/fragment.go` (extract the tag helper, if it is not already package-level)

**Interfaces:**
- Consumes: `types.Document`, `types.RenderContext`, `types.ErrNotFound`, and the existing `Execute`, `PanicError`, `SlotEngine` and `observability.Timing`.
- Produces: `render.DocumentResult` and `(*SlotEngine).ExecuteDocument(ctx, doc, rc) (*DocumentResult, error)`. Tasks 4, 5 and 8 depend on both.

Document execution lives in `internal/render` because that package already owns panic-safe execution with timeouts, metrics and tracing. It renders no templates.

**Before writing anything, read `internal/render/engine.go`** for `SlotEngine`'s field names (the metrics and default-timeout fields) and `internal/render/fragment.go` for the existing tag-sorting helper. The code below uses `e.metrics` and `e.defaultTimeout`; correct them to whatever the file actually calls them.

- [ ] **Step 1: Write the failing test**

```go
// internal/render/document_test.go
package render

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

func documentContext() *types.RenderContext {
	req := httptest.NewRequest("GET", "/sitemap.xml", nil)
	return types.NewRenderContext(context.Background(), req, nil, "en", nil)
}

func TestExecuteDocument_ReturnsBodyContentTypeAndSortedTags(t *testing.T) {
	engine := New(nil, Options{})
	doc := &types.Document{
		Name:           "sitemap",
		ContentType:    "application/xml",
		DependencyTags: []string{"site", "site"},
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return []byte("<urlset/>"), []string{"blog:posts", "blog:posts", ""}, nil
		},
	}

	result, err := engine.ExecuteDocument(context.Background(), doc, documentContext())
	if err != nil {
		t.Fatalf("ExecuteDocument() = %v", err)
	}
	if string(result.Body) != "<urlset/>" {
		t.Fatalf("Body = %q", result.Body)
	}
	if result.ContentType != "application/xml" {
		t.Fatalf("ContentType = %q, want application/xml", result.ContentType)
	}
	if want := []string{"blog:posts", "site"}; !slices.Equal(result.Tags, want) {
		t.Fatalf("Tags = %v, want %v — deduplicated, sorted, empties dropped", result.Tags, want)
	}
}

func TestExecuteDocument_NotFoundIsClassifiedNotSwallowed(t *testing.T) {
	engine := New(nil, Options{})
	doc := &types.Document{
		Name:        "post-json",
		ContentType: "application/json",
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, fmt.Errorf("looking up post: %w", types.ErrNotFound)
		},
	}

	result, err := engine.ExecuteDocument(context.Background(), doc, documentContext())
	if err == nil {
		t.Fatal("ExecuteDocument() = nil error, want the handler's error — NotFound classifies, it does not succeed")
	}
	if result == nil {
		t.Fatal("Result = nil, want a non-nil result on every path")
	}
	if !result.NotFound {
		t.Fatal("NotFound = false, want true")
	}
	if result.Body != nil {
		t.Fatalf("Body = %q, want nil on failure", result.Body)
	}
}

func TestExecuteDocument_OrdinaryErrorIsNotNotFound(t *testing.T) {
	engine := New(nil, Options{})
	doc := &types.Document{
		Name:        "feed",
		ContentType: "application/rss+xml",
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, errors.New("database unavailable")
		},
	}

	result, err := engine.ExecuteDocument(context.Background(), doc, documentContext())
	if err == nil {
		t.Fatal("ExecuteDocument() = nil, want an error")
	}
	if result.NotFound {
		t.Fatal("NotFound = true, want false for an ordinary error")
	}
}

func TestExecuteDocument_PanicIsContained(t *testing.T) {
	engine := New(nil, Options{})
	doc := &types.Document{
		Name:        "boom",
		ContentType: "text/plain",
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			panic("handler exploded")
		},
	}

	result, err := engine.ExecuteDocument(context.Background(), doc, documentContext())
	if err == nil {
		t.Fatal("ExecuteDocument() = nil, want an error")
	}
	var panicErr *PanicError
	if !errors.As(err, &panicErr) {
		t.Fatalf("error = %v, want a *PanicError", err)
	}
	if result == nil {
		t.Fatal("Result = nil, want a non-nil result even after a panic")
	}
}

func TestExecuteDocument_TimeoutBoundsAContextRespectingHandler(t *testing.T) {
	engine := New(nil, Options{DefaultTimeout: 20 * time.Millisecond})
	doc := &types.Document{
		Name:        "slow",
		ContentType: "text/plain",
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		},
	}

	start := time.Now()
	if _, err := engine.ExecuteDocument(context.Background(), doc, documentContext()); err == nil {
		t.Fatal("ExecuteDocument() = nil, want a deadline error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("took %v, want the handler bounded by its 20ms timeout", elapsed)
	}
}

func TestExecuteDocument_ReportsRenderDurationOnBothPaths(t *testing.T) {
	tests := []struct {
		name    string
		handler types.DocumentHandlerFunc
	}{
		{"success", func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return []byte("ok"), nil, nil
		}},
		{"failure", func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, errors.New("boom")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics := observability.NewRecordingMetrics()
			engine := New(nil, Options{Metrics: metrics})
			doc := &types.Document{Name: "doc", ContentType: "text/plain", Handler: test.handler}

			engine.ExecuteDocument(context.Background(), doc, documentContext())

			if got := len(metrics.Snapshot().RenderDurations); got != 1 {
				t.Fatalf("RenderDurations = %d, want 1 — an observability layer blind to failures is blind to what matters", got)
			}
		})
	}
}
```

Add `"github.com/Elagoht/collage/internal/observability"` to the test imports.

- [ ] **Step 2: Run the test and confirm it fails**

Run: `go test ./internal/render/ -run TestExecuteDocument -v`
Expected: compile failure, `engine.ExecuteDocument undefined`.

- [ ] **Step 3: Make the tag helper package-level**

Read `internal/render/fragment.go`. If the deduplicate-sort-drop-empties helper is a method on `renderState`, extract its pure part to a package-level function and have the existing caller use it:

```go
// sortedTags returns tags deduplicated, sorted and with empty entries dropped.
// Determinism here is a framework invariant: the same input must always produce
// the same tag slice, so cache invalidation is reproducible.
func sortedTags(tags []string) []string {
	// body moved verbatim from the existing helper
}
```

If it is already package-level, skip this step and reuse it as is. Do not write a second implementation.

- [ ] **Step 4: Implement `internal/render/document.go`**

```go
package render

import (
	"context"
	"errors"
	"time"

	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/types"
)

// DocumentResult is one document execution's output. Like Result, it is non-nil on
// every path including a failure, so a caller can always read Timing and NotFound.
// Check the error before reading Body.
type DocumentResult struct {
	// Body is the bytes to serve. It is nil whenever ExecuteDocument returned an
	// error.
	Body []byte
	// ContentType is copied from the document, for the caller to write verbatim.
	ContentType string
	// Tags are the dependency tags this response was derived from, deduplicated,
	// sorted and with empty entries dropped.
	Tags []string
	// NotFound reports that the handler failed with an error wrapping
	// types.ErrNotFound, meaning the response is a 404 rather than a 500. It is a
	// classification of the failure, not a success.
	NotFound bool
	// Timing records how long the handler took.
	Timing observability.Timing
}

// ExecuteDocument runs doc's handler with panic containment and doc's effective
// timeout, and returns its body, content type and collected tags. The handler runs
// on the calling goroutine — see Execute for why a spawned one would leak, and for
// the limitation that a handler ignoring its context can still overrun.
func (e *SlotEngine) ExecuteDocument(ctx context.Context, doc *types.Document, rc *types.RenderContext) (*DocumentResult, error) {
	started := time.Now()
	result := &DocumentResult{ContentType: doc.ContentType}

	var (
		body []byte
		tags []string
	)
	err := Execute(ctx, e.defaultTimeout, func(ctx context.Context) error {
		var handlerErr error
		body, tags, handlerErr = doc.Handler(ctx, rc.WithContext(ctx))
		return handlerErr
	})

	result.Timing.Total = time.Since(started)
	result.Timing.Data = result.Timing.Total
	e.metrics.RenderDuration(ctx, doc.Name, result.Timing.Total, false)

	if err != nil {
		result.NotFound = errors.Is(err, types.ErrNotFound)
		return result, err
	}

	result.Body = body
	result.Tags = sortedTags(append(append([]string(nil), tags...), doc.DependencyTags...))
	return result, nil
}
```

- [ ] **Step 5: Run the tests and confirm they pass**

Run: `go test ./internal/render/ -v -count=1 && go test ./internal/render/ -race -count=1`
Expected: PASS, including every pre-existing render test.

- [ ] **Step 6: Run the full gate and commit**

```bash
gofmt -l . && go build ./... && go vet ./... && go test ./... -count=1
git add internal/render/
git commit -m "feat(render): execute document handlers with panic safety and timeouts"
```

---

## Task 4: Serving documents over HTTP

**Files:**
- Create: `internal/httpx/document.go`, `internal/httpx/plaintext.go`, `internal/httpx/document_test.go`
- Modify: `internal/httpx/handler.go` (branch on `match.Document`), `internal/httpx/errorpage.go` (route-kind-aware error surface)

**Interfaces:**
- Consumes: `MatchResult.Document` (Task 2), `render.DocumentResult` and `ExecuteDocument` (Task 3), the existing `Deps`, `cache.Key`, `cache.ETag`, `cache.ETagMatch`, `plugin.Registry`.
- Produces: the document branch of the request lifecycle. Task 5 wires it; nothing else consumes new names from here except `ErrEmptyDocumentBody`.

**Read `internal/httpx/handler.go` first.** You are adding a branch to an existing lifecycle, not writing a new one. The page path must be untouched.

- [ ] **Step 1: Write the failing test**

```go
// internal/httpx/document_test.go
package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// documentEnv builds a Handler serving exactly one document, with a real router, a
// real *render.SlotEngine and a counting cache. Build it from the Deps construction
// helper already in handler_test.go — read that file first — registering doc via
// Router.RegisterDocument instead of Register. documentEnvWithPlugin is the same with
// a plugin registry attached. Return the cache so a test can assert it stayed empty.

func TestDocument_ServesBodyAndContentType(t *testing.T) {
	doc := &types.Document{
		Name: "sitemap", ContentType: "application/xml",
		Paths:    map[string]string{"en": "/sitemap.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return []byte("<urlset/>"), []string{"blog:posts"}, nil
		},
	}
	h, _ := documentEnv(t, doc, false)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/sitemap.xml", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/xml" {
		t.Fatalf("Content-Type = %q, want application/xml", got)
	}
	if rec.Body.String() != "<urlset/>" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("ETag is empty, want a content hash")
	}
}

func TestDocument_CacheHitDoesNotReExecuteTheHandler(t *testing.T) {
	var calls int
	doc := &types.Document{
		Name: "sitemap", ContentType: "application/xml",
		Paths:    map[string]string{"en": "/sitemap.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			calls++
			return []byte("<urlset/>"), nil, nil
		},
	}
	h, _ := documentEnv(t, doc, false)

	for range 2 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/sitemap.xml", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times across two requests, want 1 — the cache is not being consulted", calls)
	}
}

func TestDocument_NotModified(t *testing.T) {
	doc := &types.Document{
		Name: "sitemap", ContentType: "application/xml",
		Paths:    map[string]string{"en": "/sitemap.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return []byte("<urlset/>"), nil, nil
		},
	}
	h, _ := documentEnv(t, doc, false)

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest("GET", "/sitemap.xml", nil))
	etag := first.Header().Get("ETag")

	req := httptest.NewRequest("GET", "/sitemap.xml", nil)
	req.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	h.ServeHTTP(second, req)

	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty on 304", second.Body.String())
	}
}

func TestDocument_NotFoundIsPlainTextAndUncached(t *testing.T) {
	doc := &types.Document{
		Name: "post-json", ContentType: "application/json",
		Paths:    map[string]string{"en": "/post.json"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, fmt.Errorf("lookup: %w", types.ErrNotFound)
		},
	}
	h, store := documentEnv(t, doc, false)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/post.json", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain — never HTML, and never the document's own type", ct)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if store.entries() != 0 {
		t.Fatalf("cache holds %d entries after a 404, want 0", store.entries())
	}
}

func TestDocument_ProductionFailureLeaksNothing(t *testing.T) {
	doc := &types.Document{
		Name: "feed", ContentType: "application/rss+xml",
		Paths:    map[string]string{"en": "/rss.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, errors.New("dial tcp db.internal:5432: password=hunter2 /srv/app/store.go")
		},
	}
	h, store := documentEnv(t, doc, false)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/rss.xml", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
	for _, leak := range []string{"dial tcp", "hunter2", "db.internal", "/srv/app", "feed", "collage/internal", "goroutine "} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Fatalf("production 500 body leaks %q: %q", leak, rec.Body.String())
		}
	}
	if store.entries() != 0 {
		t.Fatalf("cache holds %d entries after a 500, want 0", store.entries())
	}
}

func TestDocument_DevFailureShowsTheDetail(t *testing.T) {
	doc := &types.Document{
		Name: "feed", ContentType: "application/rss+xml",
		Paths:    map[string]string{"en": "/rss.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, errors.New("dial tcp db.internal:5432")
		},
	}
	h, _ := documentEnv(t, doc, true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/rss.xml", nil))

	for _, want := range []string{"feed", "dial tcp"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("dev 500 body should contain %q: %q", want, rec.Body.String())
		}
	}
}

func TestDocument_HeadWritesHeadersWithoutBody(t *testing.T) {
	doc := &types.Document{
		Name: "robots", ContentType: "text/plain; charset=utf-8",
		Paths:    map[string]string{"en": "/robots.txt"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return []byte("User-agent: *\n"), nil, nil
		},
	}
	h, _ := documentEnv(t, doc, false)

	server := httptest.NewServer(h)
	defer server.Close()

	resp, err := http.Head(server.URL + "/robots.txt")
	if err != nil {
		t.Fatalf("HEAD = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if resp.ContentLength != int64(len("User-agent: *\n")) {
		t.Fatalf("Content-Length = %d, want %d — a HEAD reports what a GET would send", resp.ContentLength, len("User-agent: *\n"))
	}
}

func TestDocument_RenderHooksDoNotFire(t *testing.T) {
	doc := &types.Document{
		Name: "sitemap", ContentType: "application/xml",
		Paths:    map[string]string{"en": "/sitemap.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return []byte("<urlset/>"), []string{"blog:posts"}, nil
		},
	}
	// recordingPlugin implements every hook and appends its name to a slice; it
	// already exists in handler_test.go. Reuse it rather than writing a second one.
	plugin := newRecordingPlugin()
	h := documentEnvWithPlugin(t, doc, plugin)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/sitemap.xml", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	called := plugin.calls()
	for _, forbidden := range []string{"OnPageResolved", "OnBeforeRender", "OnAfterRender"} {
		if slices.Contains(called, forbidden) {
			t.Fatalf("%s fired for a document; hooks called: %v — no render occurs, and "+
				"AfterRenderEvent.HTML would be a lie for a non-HTML body", forbidden, called)
		}
	}
	if !slices.Contains(called, "OnCacheWrite") {
		t.Fatalf("OnCacheWrite did not fire; hooks called: %v", called)
	}
}
```

`countingCache` and the recording plugin already exist in `internal/httpx/handler_test.go`. Read that file and reuse them; write `documentEnv` in terms of the existing `Deps` construction helper there.

- [ ] **Step 2: Run the test and confirm it fails**

Run: `go test ./internal/httpx/ -run TestDocument -v`
Expected: failures — documents route nowhere yet, so every request 404s with the HTML page.

- [ ] **Step 3: Write `internal/httpx/plaintext.go`**

```go
package httpx

import (
	"fmt"
	"net/http"
	"strconv"
)

// writePlainText writes a plain-text error response for a document or an asset.
// The content type of an error follows the route kind, not the request: an HTML
// error page returned to a crawler fetching sitemap.xml, or to a client expecting
// JSON, is the same mistake. In production the body is a single generic line; in
// dev mode it names the route and carries the error chain.
func writePlainText(w http.ResponseWriter, r *http.Request, status int, devMode bool, route string, cause error) {
	body := http.StatusText(status) + "\n"
	if devMode && cause != nil {
		body = fmt.Sprintf("%s\n\nroute: %s\nerror: %+v\n", http.StatusText(status), route, cause)
	}

	header := w.Header()
	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)

	if r.Method != http.MethodHead {
		w.Write([]byte(body))
	}
}
```

- [ ] **Step 4: Write `internal/httpx/document.go`**

The document branch mirrors the page branch's shape. Read `serve` in `handler.go` and follow it step for step, omitting the render hooks:

```go
package httpx

import (
	"net/http"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/types"
)

// serveDocument handles a request that matched a document. It runs the same
// lifecycle as a page — cache lookup, ETag, conditional response, execute, cache
// write, tag tracking — minus the render hooks, which do not fire for a document
// because no render occurs. See plugin.BeforeRenderHook's doc comment.
func (h *Handler) serveDocument(w http.ResponseWriter, r *http.Request, match *router.MatchResult) int {
	doc := match.Document

	key := cache.Key(cache.KeyInput{
		Path:   r.URL.Path,
		Locale: match.Locale,
		Params: match.PathParams,
		Vary:   []string{r.URL.RawQuery},
	})

	if content, etag, found := h.lookupCache(r, doc.Strategy, key); found {
		return h.serveCachedDocument(w, r, doc, content, etag)
	}

	rc := types.NewRenderContext(r.Context(), r, nil, match.Locale, match.PathParams)
	result, err := h.renderer.ExecuteDocument(r.Context(), doc, rc)
	if err != nil {
		status := http.StatusInternalServerError
		if result != nil && result.NotFound {
			status = http.StatusNotFound
		}
		h.reportError(r, failure{status: status, err: err, stage: stageRender, document: doc})
		writePlainText(w, r, status, h.devMode, doc.Name, err)
		return status
	}

	etag := h.writeDocumentCache(r, key, doc, result)
	return h.writeDocument(w, r, doc, result.Body, etag)
}
```

Fill in `lookupCache`, `serveCachedDocument`, `writeDocumentCache` and `writeDocument` by extracting or mirroring the page equivalents in `handler.go`. Four rules that must hold and that the tests above pin:

- `Content-Type` is `doc.ContentType`, written verbatim.
- `Cache-Control` follows the strategy exactly as pages do, including the `StrategyStatic` never-expires TTL.
- The cache write dispatches `OnCacheWrite` and honours `Skip`, and `Tracker.Track` runs on both the tagged and untagged paths.
- Only GET populates the cache; HEAD may read it.

**The `failure` struct needs one new field.** Read `internal/httpx/errorpage.go:19`: it
currently holds `status`, `err`, `page`, `stage` and `fragment`, and `reportError` takes
`(r, f)` — the status travels *inside* the struct, not as a third argument. Add:

```go
	// document is the document being served when the failure happened, or nil when a
	// page was being served or nothing had been resolved yet. At most one of page and
	// document is ever set.
	document *types.Document
```

and extend `reportError`'s log record and its `plugin.ErrorEvent` dispatch to name the
document when it is set, so an operator chasing a failing sitemap sees which one.
`ErrorEvent` carries `Page`; leave that nil for a document rather than widening the
plugin event — the event's `Path` already identifies the route, and widening a public
plugin struct for a log line is not worth it.

In `handler.go`'s `serve`, add the branch immediately after the redirect and not-found checks:

```go
	if match.Document != nil {
		return h.serveDocument(w, r, match)
	}
```

`Deps.Renderer` currently types the page engine. Widen the interface it names to include `ExecuteDocument(ctx, *types.Document, *types.RenderContext) (*render.DocumentResult, error)`; `*render.SlotEngine` already satisfies it after Task 3.

- [ ] **Step 5: Run the tests and confirm they pass**

Run: `go test ./internal/httpx/ -v -count=1 && go test ./internal/httpx/ -race -count=1`
Expected: PASS, including every pre-existing handler test. The page lifecycle must be unchanged.

- [ ] **Step 6: Mutation-check the leak test**

Temporarily force `devMode` true inside `writePlainText` and re-run `TestDocument_ProductionFailureLeaksNothing`. Confirm it **fails**. Revert. A leak test that passes in both modes tests nothing.

- [ ] **Step 7: Run the full gate and commit**

```bash
gofmt -l . && go build ./... && go vet ./... && go test ./... -count=1
git add internal/httpx/
git commit -m "feat(httpx): serve documents with plain-text errors"
```

---

## Task 5: Registering documents on the App

**Files:**
- Create: `internal/core/document.go`, `pkg/collage/document.go`, `pkg/collage/document_test.go`
- Modify: `internal/core/app.go` (`buildHandler` close-out), `internal/core/registry.go` (registration state)
- Test: `internal/core/document_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1-4.
- Produces: `(*App).RegisterDocument`, `(*App).Documents`, `(*App).RenderDocumentPath`, and in `pkg/collage`: `Document`, `DocumentHandlerFunc`, `NewDocument`, `DocumentBuilder`, plus the re-exported sentinels. Task 8 consumes `Documents` and `RenderDocumentPath`.

- [ ] **Step 1: Write the failing test**

Write each of these in full, mirroring the existing page tests in
`internal/core/app_test.go` — build the App over a `t.TempDir()` template directory
exactly as they do. The precise assertion each one must make:

| Test | Assertion |
|---|---|
| `TestRegisterDocument_RejectsADuplicateName` | second `RegisterDocument` with the same `Name` returns an error satisfying `errors.Is(err, ErrDuplicateDocument)` and the message names the document |
| `TestRegisterDocument_RejectsAfterStart` | call `Handler()` first, then `RegisterDocument` → `errors.Is(err, ErrAppStarted)` |
| `TestRegisterDocument_RejectsAnInvalidDocument` | a document with `ContentType: ""` → `errors.Is(err, types.ErrEmptyContentType)` and the message contains the document's name |
| `TestApp_ServesADocumentEndToEnd` | `httptest` request through `Handler()` → 200, `Content-Type` exactly the document's, body exactly the handler's bytes, non-empty `ETag` |
| `TestApp_InvalidateTagsRegeneratesADocument` | handler increments a counter; request twice → counter is 1; `InvalidateTags(ctx, "blog:posts")`; request again → counter is 2. A status code cannot distinguish a cache hit from a re-execution, so the counter is the assertion |
| `TestRenderDocumentPath_UsesTheRoutersLocale` | register a document at `/tr/site-haritasi.xml`; call `RenderDocumentPath(ctx, "/tr/site-haritasi.xml", "en", nil)` → either the result's locale is `tr` or the call returns an error. It must never render the `tr` route while telling the handler the locale is `en` — the same rule already ruled for `RenderPath` |

```go
// pkg/collage/document_test.go
func TestNewDocument_BuildsTheSpecShape(t *testing.T) {
	doc := collage.NewDocument("sitemap", "application/xml").
		WithPath("en", "/sitemap.xml").
		WithHandler(func(ctx context.Context, rc *collage.RenderContext) ([]byte, []string, error) {
			return []byte("<urlset/>"), []string{"blog:posts"}, nil
		}).
		Incremental(time.Hour).
		WithDependency("site").
		Build()

	if doc.Name != "sitemap" || doc.ContentType != "application/xml" {
		t.Fatalf("doc = %+v", doc)
	}
	if doc.Paths["en"] != "/sitemap.xml" {
		t.Fatalf("Paths = %v", doc.Paths)
	}
	if doc.Strategy != collage.StrategyIncremental || doc.CacheTTL != time.Hour {
		t.Fatalf("strategy = %v ttl = %v", doc.Strategy, doc.CacheTTL)
	}
}

func TestDocumentBuilder_BuildErrReportsAMissingHandler(t *testing.T) {
	b := collage.NewDocument("sitemap", "application/xml").WithPath("en", "/sitemap.xml")
	b.Build()
	if !errors.Is(b.BuildErr(), collage.ErrNoDocumentHandler) {
		t.Fatalf("BuildErr() = %v, want ErrNoDocumentHandler", b.BuildErr())
	}
}
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./internal/core/ ./pkg/collage/ -run Document -v`
Expected: compile failure.

- [ ] **Step 3: Implement `internal/core/document.go`**

`RegisterDocument` mirrors `RegisterPage`'s `prepare` exactly, minus the layout binding and the template check (a document has no template):

```go
// RegisterDocument validates doc and adds it to the router. It returns
// ErrAppStarted after the server has started, ErrDuplicateDocument for a repeated
// name, ErrDuplicateRoute when a path collides with a page or another document, and
// the document's own validation error otherwise — always naming the document.
func (a *App) RegisterDocument(doc *types.Document) error
```

`Documents() []*types.Document` returns a copy of the slice.

`RenderDocumentPath(ctx, path, locale string, params map[string]string) (*render.DocumentResult, error)` resolves through the router and **uses the router's resolved locale**, rejecting a caller locale that disagrees with the matched route — the same rule ruled for `RenderPath`. Bypasses the cache.

Add a `bound`-style duplicate-name map beside the existing page one, and extend `buildHandler`'s close-out check to cover documents.

- [ ] **Step 4: Implement `pkg/collage/document.go`**

Aliases and the builder, following `pkg/collage/page.go`'s established accumulate-errors shape:

```go
type Document = types.Document
type DocumentHandlerFunc = types.DocumentHandlerFunc

var (
	ErrNilDocument       = types.ErrNilDocument
	ErrEmptyContentType  = types.ErrEmptyContentType
	ErrNoDocumentHandler = types.ErrNoDocumentHandler
	ErrDuplicateDocument = core.ErrDuplicateDocument
)

// NewDocument starts building a document served at contentType.
func NewDocument(name, contentType string) *DocumentBuilder
```

with `WithPath`, `WithHandler`, `WithRedirect`, `WithPermanentRedirect`, `Static`, `Dynamic`, `Incremental`, `WithDependency`, `Build`, `BuildErr`.

- [ ] **Step 5: Run the tests, then the full gate, then commit**

```bash
go test ./internal/core/ ./pkg/collage/ -v -count=1
go test ./internal/core/ ./pkg/collage/ -race -count=1
gofmt -l . && go build ./... && go vet ./... && go test ./... -count=1
git add internal/core/ pkg/collage/
git commit -m "feat(core): register and serve documents through the App"
```

---

## Task 6: The asset mount handler

**Files:**
- Create: `internal/asset/mount.go`, `internal/asset/etag.go`, `internal/asset/mount_test.go`, `internal/asset/etag_test.go`
- Test fixtures: `internal/asset/testdata/`

**Interfaces:**
- Consumes: `io/fs`, `net/http`, `mime`, `crypto/sha256` only. This package imports **nothing** from the rest of the project — it is a leaf, like `internal/types`.
- Produces: `asset.Mount`, `asset.New(prefix string, fsys fs.FS, opts ...Option) (*Mount, error)`, `(*Mount).ServeHTTP`, `(*Mount).Prefix()`, `(*Mount).FS()`, `(*Mount).BuildCopy() bool`, and the sentinels `ErrInvalidPrefix`, `ErrNilFS`. Tasks 7 and 8 depend on these.

Assets are served with `http.ServeContent`, which provides `Range`, `If-Range`, 206 and `Last-Modified` — the reason audio seeking works. They never enter the page cache.

- [ ] **Step 1: Write the failing tests**

```go
// internal/asset/mount_test.go
package asset

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fs.FS {
	return fstest.MapFS{
		"app.css":         {Data: []byte("body{color:red}")},
		"audio/track.mp3": {Data: []byte("0123456789abcdefghijklmnopqrstuvwxyz")},
		"nested/deep/a.txt": {Data: []byte("deep")},
	}
}

func mustMount(t *testing.T, opts ...Option) *Mount {
	t.Helper()
	m, err := New("/static/", testFS(), opts...)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	return m
}

func TestMount_ServesAFile(t *testing.T) {
	m := mustMount(t)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/static/app.css", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "body{color:red}" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("Content-Type = %q, want text/css", ct)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("ETag is empty — embed.FS has a zero ModTime, so an ETag is the only validator that works")
	}
}

func TestMount_RangeRequestReturns206(t *testing.T) {
	// A ResponseRecorder cannot show this properly; use a real server.
	server := httptest.NewServer(mustMount(t))
	defer server.Close()

	req, _ := http.NewRequest("GET", server.URL+"/static/audio/track.mp3", nil)
	req.Header.Set("Range", "bytes=10-19")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206 — seeking in audio is a Range request", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "abcdefghij" {
		t.Fatalf("body = %q, want the requested byte range", body)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 10-19/36" {
		t.Fatalf("Content-Range = %q", got)
	}
}

func TestMount_NoDirectoryListing(t *testing.T) {
	m := mustMount(t)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/static/audio/", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — a directory listing is an information leak", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "track.mp3") {
		t.Fatalf("body leaks a directory listing: %q", rec.Body.String())
	}
}

func TestMount_RejectsTraversal(t *testing.T) {
	tests := []string{
		"/static/../secret",
		"/static/nested/../../secret",
		"/static//etc/passwd",
		"/static/./../../secret",
	}
	m := mustMount(t)

	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			rec := httptest.NewRecorder()
			m.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
			if rec.Code == http.StatusOK {
				t.Fatalf("status = 200 for %q, want a refusal", target)
			}
		})
	}
}

func TestMount_MethodNotAllowed(t *testing.T) {
	m := mustMount(t)
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			m.ServeHTTP(rec, httptest.NewRequest(method, "/static/app.css", nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405", rec.Code)
			}
			if rec.Header().Get("Allow") != "GET, HEAD" {
				t.Fatalf("Allow = %q", rec.Header().Get("Allow"))
			}
		})
	}
}

func TestMount_CacheControl(t *testing.T) {
	m := mustMount(t, WithCacheControl("public, max-age=31536000, immutable"))
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/static/app.css", nil))

	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestMount_NotFoundIsPlainText(t *testing.T) {
	m := mustMount(t)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/static/missing.css", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain — consistent with documents, never HTML", ct)
	}
}

func TestNew_RejectsBadPrefixes(t *testing.T) {
	for _, prefix := range []string{"", "/", "static/", "//static/"} {
		t.Run(prefix, func(t *testing.T) {
			if _, err := New(prefix, testFS()); err == nil {
				t.Fatalf("New(%q) = nil error, want ErrInvalidPrefix", prefix)
			}
		})
	}
}

func TestNew_RejectsANilFS(t *testing.T) {
	if _, err := New("/static/", nil); err == nil {
		t.Fatal("New() = nil error, want ErrNilFS")
	}
}
```

```go
// internal/asset/etag_test.go
package asset

import (
	"testing"
	"testing/fstest"
)

func TestETag_IsStableAndContentDerived(t *testing.T) {
	fsys := fstest.MapFS{"a.txt": {Data: []byte("hello")}, "b.txt": {Data: []byte("world")}}
	tags := newETagCache()

	first, err := tags.get(fsys, "a.txt")
	if err != nil {
		t.Fatalf("get() = %v", err)
	}
	second, err := tags.get(fsys, "a.txt")
	if err != nil {
		t.Fatalf("get() = %v", err)
	}
	if first != second {
		t.Fatalf("etag changed between calls: %q then %q", first, second)
	}
	other, _ := tags.get(fsys, "b.txt")
	if first == other {
		t.Fatal("different content produced the same etag")
	}
	if first[0] != '"' || first[len(first)-1] != '"' {
		t.Fatalf("etag %q must be quoted per the HTTP grammar", first)
	}
}

func TestETag_MemoisesWithoutRetainingBodies(t *testing.T) {
	fsys := fstest.MapFS{"a.txt": {Data: []byte("hello")}}
	tags := newETagCache()

	if _, err := tags.get(fsys, "a.txt"); err != nil {
		t.Fatalf("get() = %v", err)
	}
	if got := tags.len(); got != 1 {
		t.Fatalf("cache holds %d entries, want 1", got)
	}
	// The cache stores hashes, never bodies: entry size is bounded by file count.
}
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./internal/asset/ -v`
Expected: the package does not exist.

- [ ] **Step 3: Implement `internal/asset/etag.go`**

```go
package asset

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"sync"
)

// etagCache memoises content-hash ETags per path. It stores hashes, never bodies,
// so its footprint is bounded by file count rather than by total asset size.
//
// A content hash is used rather than size and modification time because embed.FS
// reports a zero ModTime for every file, which would silently disable
// modification-time validation for the most common asset source.
type etagCache struct {
	mu   sync.RWMutex
	tags map[string]string
}

func newETagCache() *etagCache {
	return &etagCache{tags: make(map[string]string)}
}

// get returns the quoted strong ETag for name within fsys, hashing the file on
// first use.
func (c *etagCache) get(fsys fs.FS, name string) (string, error) {
	c.mu.RLock()
	tag, ok := c.tags[name]
	c.mu.RUnlock()
	if ok {
		return tag, nil
	}

	file, err := fsys.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	tag = `"` + hex.EncodeToString(sum.Sum(nil)[:16]) + `"`

	c.mu.Lock()
	c.tags[name] = tag
	c.mu.Unlock()
	return tag, nil
}

func (c *etagCache) len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.tags)
}
```

- [ ] **Step 4: Implement `internal/asset/mount.go`**

```go
package asset

import (
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// ErrInvalidPrefix reports a mount prefix that is empty, "/", or not a
// slash-delimited path. A mount at "/" would swallow every route.
var ErrInvalidPrefix = errors.New("collage: invalid mount prefix")

// ErrNilFS reports that a mount was given no file system.
var ErrNilFS = errors.New("collage: nil mount file system")

// Option configures a Mount.
type Option func(*Mount)

// WithCacheControl sets the Cache-Control header served with every file.
func WithCacheControl(value string) Option {
	return func(m *Mount) { m.cacheControl = value }
}

// WithoutBuildCopy stops a static build from copying this mount into its output.
// Use it for a mount served from a CDN in production, or one large enough that
// duplicating it into the build directory is not wanted.
func WithoutBuildCopy() Option {
	return func(m *Mount) { m.buildCopy = false }
}

// Mount serves an fs.FS under a URL prefix using http.ServeContent, which
// provides Range, If-Range, 206 and Last-Modified handling. Mounted files never
// enter the page cache: their freshness is the client's and the mount's
// Cache-Control's business, not the framework's.
type Mount struct {
	prefix       string
	fsys         fs.FS
	cacheControl string
	buildCopy    bool
	tags         *etagCache
}

// New returns a Mount serving fsys under prefix. prefix must begin and end with
// "/" and must not be "/" alone.
func New(prefix string, fsys fs.FS, opts ...Option) (*Mount, error) {
	if prefix == "" || prefix == "/" || !strings.HasPrefix(prefix, "/") || !strings.HasSuffix(prefix, "/") || strings.HasPrefix(prefix, "//") {
		return nil, ErrInvalidPrefix
	}
	if fsys == nil {
		return nil, ErrNilFS
	}

	m := &Mount{
		prefix:       prefix,
		fsys:         fsys,
		cacheControl: "public, max-age=3600",
		buildCopy:    true,
		tags:         newETagCache(),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m, nil
}

// Prefix returns the URL prefix this mount serves under.
func (m *Mount) Prefix() string { return m.prefix }

// FS returns the file system this mount serves.
func (m *Mount) FS() fs.FS { return m.fsys }

// BuildCopy reports whether a static build should copy this mount into its output.
func (m *Mount) BuildCopy() bool { return m.buildCopy }

// Handles reports whether urlPath falls under this mount's prefix.
func (m *Mount) Handles(urlPath string) bool {
	return strings.HasPrefix(urlPath, m.prefix)
}

// ServeHTTP implements http.Handler.
func (m *Mount) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		plainText(w, r, http.StatusMethodNotAllowed)
		return
	}

	name, ok := m.resolve(r.URL.Path)
	if !ok {
		plainText(w, r, http.StatusNotFound)
		return
	}

	file, err := m.fsys.Open(name)
	if err != nil {
		plainText(w, r, http.StatusNotFound)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		// A directory listing is an information leak; there is no implicit
		// index.html either. Both are deliberate.
		plainText(w, r, http.StatusNotFound)
		return
	}

	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		plainText(w, r, http.StatusInternalServerError)
		return
	}

	if tag, err := m.tags.get(m.fsys, name); err == nil {
		w.Header().Set("ETag", tag)
	}
	if ctype := mime.TypeByExtension(path.Ext(name)); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	w.Header().Set("Cache-Control", m.cacheControl)

	http.ServeContent(w, r, name, info.ModTime(), seeker)
}

// resolve turns a request path into a path within the mount's file system, or
// reports that it is not servable. Containment is not a string problem: the
// cleaned path must still be a valid fs path, which fs.ValidPath enforces by
// rejecting "..", absolute paths and empty elements.
func (m *Mount) resolve(urlPath string) (string, bool) {
	if !strings.HasPrefix(urlPath, m.prefix) {
		return "", false
	}
	name := path.Clean(strings.TrimPrefix(urlPath, m.prefix))
	if name == "." || name == "/" || strings.HasPrefix(name, "/") {
		return "", false
	}
	if !fs.ValidPath(name) {
		return "", false
	}
	return name, true
}

// plainText writes an error body matching the framework's document error surface:
// never HTML, so a client fetching a stylesheet or a media file is not handed a
// web page.
func plainText(w http.ResponseWriter, r *http.Request, status int) {
	body := http.StatusText(status) + "\n"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write([]byte(body))
	}
}
```

Note: `fs.File` is not guaranteed to be an `io.ReadSeeker`. `os.DirFS`, `os.Root.FS()` and `embed.FS` all return files that are, and `fstest.MapFS` does too, so the assertion succeeds for every supported source. The 500 branch exists so an exotic `fs.FS` fails loudly rather than silently serving nothing.

- [ ] **Step 5: Run the tests and confirm they pass**

Run: `go test ./internal/asset/ -v -count=1 && go test ./internal/asset/ -race -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go build ./... && go vet ./... && go test ./... -count=1
git add internal/asset/
git commit -m "feat(asset): serve mounted file systems with Range support"
```

---

## Task 7: Mounting assets on the App

**Files:**
- Create: `internal/core/mount.go`, `pkg/collage/mount.go`, `internal/core/mount_test.go`
- Modify: `internal/core/app.go` (`buildHandler` close-out, `Deps` construction), `internal/httpx/handler.go` (check mounts before routing)

**Interfaces:**
- Consumes: `asset.Mount`, `asset.New` (Task 6); the existing close-out check added in the previous plan.
- Produces: `(*App).Mount(prefix string, fsys fs.FS, opts ...asset.Option) error`, `(*App).Mounts() []*asset.Mount`, and the sentinels `ErrMountShadowsRoute`, `ErrMountConflict`. Task 8 consumes `Mounts`.

- [ ] **Step 1: Write the failing test**

Write each in full, following `internal/core/app_test.go`'s helpers. The assertions:

| Test | Assertion |
|---|---|
| `TestMount_ServesAFileAlongsidePages` | a page at `/` and a mount at `/static/`; both serve correctly in one App |
| `TestMount_RejectsAPrefixShadowingARegisteredPage` | page at `/static/thing`, then `Mount("/static/")`, then `Handler()` → `errors.Is(err, ErrMountShadowsRoute)` and the message names both the mount prefix and the page |
| `TestMount_RejectsAPrefixShadowingADocument` | identical, with a document at `/static/data.json` |
| `TestMount_ShadowCheckIsOrderIndependent` | **mount first**, then register the page at `/static/thing`, then `Handler()` → still `ErrMountShadowsRoute`. This proves the check runs at the close-out point rather than at call time, which is the whole reason it lives there |
| `TestMount_RejectsTwoOverlappingMounts` | `Mount("/static/")` and `Mount("/static/img/")` → `errors.Is(err, ErrMountConflict)` |
| `TestMount_RejectsAfterStart` | `Handler()` first, then `Mount` → `errors.Is(err, ErrAppStarted)` |
| `TestMount_DoesNotEnterThePageCache` | serve a mounted file, then assert the page cache holds zero entries. Assets bypassing the cache is the entire reason the mechanism is separate, so it needs pinning |

- [ ] **Step 2: Run and confirm failure**

- [ ] **Step 3: Implement `internal/core/mount.go`**

```go
// ErrMountShadowsRoute reports that a mount prefix would swallow a registered page
// or document path, making that route unreachable.
var ErrMountShadowsRoute = errors.New("collage: mount shadows a route")

// ErrMountConflict reports that two mounts claim overlapping prefixes.
var ErrMountConflict = errors.New("collage: mount prefixes overlap")

// Mount serves fsys under prefix. It returns ErrAppStarted after the server has
// started. Conflicts with routes are detected when the handler is built, not here,
// so registration order does not matter.
func (a *App) Mount(prefix string, fsys fs.FS, opts ...asset.Option) error
```

Store mounts on the App. In `buildHandler`, beside the existing error-page close-out check, add `checkMountsDoNotShadow()`: for every mount prefix, every registered page path and every document path, fail if a route path begins with a mount prefix, naming both. Also fail if two mount prefixes prefix each other.

- [ ] **Step 4: Wire mounts into the handler**

`httpx.Deps` gains `Mounts []*asset.Mount`. In `ServeHTTP`, before routing:

```go
	for _, mount := range h.mounts {
		if mount.Handles(r.URL.Path) {
			mount.ServeHTTP(w, r)
			return
		}
	}
```

Mounts are checked before the router because their prefixes are guaranteed not to shadow any route — the close-out check enforces exactly that, so the order is safe and O(mounts).

- [ ] **Step 5: Implement `pkg/collage/mount.go`**

```go
type MountOption = asset.Option

var (
	WithCacheControl  = asset.WithCacheControl
	WithoutBuildCopy  = asset.WithoutBuildCopy

	ErrInvalidPrefix     = asset.ErrInvalidPrefix
	ErrNilFS             = asset.ErrNilFS
	ErrMountShadowsRoute = core.ErrMountShadowsRoute
	ErrMountConflict     = core.ErrMountConflict
)
```

`App.Mount` is reachable through the existing `type App = core.App` alias.

- [ ] **Step 6: Run the tests, the full gate, and commit**

```bash
go test ./internal/core/ ./internal/httpx/ ./pkg/collage/ -race -count=1
gofmt -l . && go build ./... && go vet ./... && go test ./... -count=1
git add internal/core/ internal/httpx/ pkg/collage/
git commit -m "feat(core): mount asset file systems alongside routes"
```

---

## Task 8: Static build for documents and assets

**Files:**
- Create: `internal/build/document.go`, `internal/build/asset.go`, `internal/build/document_test.go`, `internal/build/asset_test.go`
- Modify: `internal/build/builder.go` (`Renderer`, `Options`, the build loop), `pkg/collage/build.go` (re-export the new provider)

**Interfaces:**
- Consumes: `(*App).Documents`, `(*App).RenderDocumentPath` (Task 5), `(*App).Mounts` (Task 7).
- Produces: `build.DocumentPathProvider`, `Options.DocumentPathProvider`, and the widened `build.Renderer`.

Two signature changes, both named in the spec so they are not a surprise:

- `Renderer` gains `Documents() []*types.Document` and `RenderDocumentPath(...)`. `Renderer` is internal-only (it is not re-exported through `pkg/collage`), so widening it breaks no user.
- `PathProvider` is **not** changed. It is public API. Documents get a separate optional interface instead, following the `TaggedCache` precedent:

```go
// DocumentPathProvider supplies the concrete paths a dynamic document's pattern
// expands to. It is optional: a build without one skips dynamic documents and
// records why. It is deliberately separate from PathProvider rather than a
// widening of it, so an existing PathProvider implementation keeps compiling.
type DocumentPathProvider interface {
	Paths(ctx context.Context, doc *types.Document, locale string) ([]PathInstance, error)
}
```

- [ ] **Step 1: Write the failing tests**

Write each in full, using `t.TempDir()` for output as the existing builder tests do.
The assertions:

| Test | Assertion |
|---|---|
| `TestBuild_WritesADocumentToItsLiteralPath` | `/sitemap.xml` produces `<out>/sitemap.xml` with the handler's exact bytes, and **`<out>/sitemap.xml/index.html` does not exist**. Assert the negative explicitly — writing a directory where a crawler expects a file is the mistake this test exists to catch |
| `TestBuild_SkipsDynamicStrategyDocuments` | a `StrategyDynamic` document appears in `Report.Skipped` with a reason, and nothing is written for it |
| `TestBuild_SkipsADynamicPatternWithoutAProvider` | a document at `/api/{id}.json` with no `DocumentPathProvider` → a `Skipped` record wrapping `ErrDynamicPathUnresolved`, and the build still succeeds |
| `TestBuild_UsesTheDocumentPathProvider` | the same document with a provider returning two ids writes both files |
| `TestBuild_ADocumentPathCannotEscapeOutDir` | a provider returning `../escape` → refused, and nothing exists outside `OutDir` afterwards |
| `TestBuild_ARefusedDocumentDoesNotStopTheBuild` | two documents, the first failing → the second is still written and the joined error names the first |
| `TestBuild_CopiesMountedAssets` | `<out>/static/app.css` exists with the mount's exact bytes |
| `TestBuild_HonoursWithoutBuildCopy` | with `WithoutBuildCopy()`, `<out>/static/` does not exist |
| `TestBuild_AssetCopyCannotEscapeOutDir` | an `fs.FS` whose `WalkDir` yields a name that would escape → refused, nothing written outside `OutDir` |

The two escape tests must reuse the containment helpers already in `builder.go` —
`resolveTarget` and `verifyNoSymlinksBeneath`. Do not write a second containment check: a
second implementation is exactly how the page and asset paths would drift, and this
project has already fixed two symlink escapes that a shared helper would have prevented.

- [ ] **Step 2: Run and confirm failure**

- [ ] **Step 3: Implement `internal/build/document.go`**

Mirror the page loop. The one behavioural difference: the output path is the URL path itself, not `path/index.html`.

```go
// documentTarget returns the file a document's path is written to. Unlike a page,
// a document writes to its literal path: /sitemap.xml becomes <OutDir>/sitemap.xml,
// because a crawler asking for /sitemap.xml must not receive a directory.
func documentTarget(outDir, urlPath string) (string, error)
```

Route it through the same `resolveTarget` containment check pages use.

- [ ] **Step 4: Implement `internal/build/asset.go`**

Walk each mount's `fs.FS` with `fs.WalkDir`, skipping mounts whose `BuildCopy()` is false, and copy each file to `<OutDir>/<prefix><name>` through the same containment check. A mount's `fs.FS` is user-supplied, so its names are untrusted input exactly as a `PathProvider`'s are.

- [ ] **Step 5: Run the tests, the full gate, and commit**

```bash
go test ./internal/build/ -race -count=1
gofmt -l . && go build ./... && go vet ./... && go test ./... -count=1
git add internal/build/ pkg/collage/
git commit -m "feat(build): write documents and copy mounted assets"
```

---

## Task 9: Documentation, example and scaffold

**Files:**
- Create: `docs/documents.md`, `docs/assets.md`
- Modify: `README.md`, `docs/architecture.md`, `docs/caching.md`, `docs/cli.md`, `examples/blog/`, `internal/cli/scaffold/`
- Test: `examples/blog/main_test.go`

**Interfaces:** Consumes the whole public surface. Produces no new code interfaces.

- [ ] **Step 1: Extend the blog example**

Add to `examples/blog`, using only `pkg/collage` and the standard library:

- `/sitemap.xml` — a `Document` tagged `blog:posts`, incremental, regenerating after `InvalidateTags`
- `/robots.txt` — a static `Document`
- `/static/` — a mount, served from an `embed.FS` so the example runs from anywhere, with a real stylesheet the layout links to

- [ ] **Step 2: Extend `examples/blog/main_test.go`**

Write each in full against `app.Handler()` with `httptest`, as the existing example
tests do. The assertions:

| Test | Assertion |
|---|---|
| `TestSitemapRendersAndCarriesItsContentType` | 200, `Content-Type: application/xml`, body contains every post's slug |
| `TestSitemapRegeneratesAfterInvalidateTags` | request twice → the handler ran once; `InvalidateTags(ctx, "blog:posts")`; request again → it ran twice |
| `TestRobotsTxtRenders` | 200, `Content-Type` starts with `text/plain`, body is the expected directives |
| `TestStylesheetIsServedFromTheMount` | 200, `Content-Type` starts with `text/css`, body matches the embedded file, non-empty `ETag` |
| `TestUnknownAssetIsPlainTextNotHTML` | `/static/nope.css` → 404, `Content-Type` starts with `text/plain`, and the body contains no `<html` — a missing stylesheet must not return a web page |

- [ ] **Step 3: Write `docs/documents.md` and `docs/assets.md`**

Every snippet must compile — check each in a scratch module with a `replace` directive, as the previous plan's documentation task did.

`docs/assets.md` must carry this warning prominently, because this project has shipped and fixed this bug class twice:

> **`os.DirFS` is not a security boundary.** Go's documentation states it does not prevent symlink traversal: a symlink placed inside the mounted directory escapes it. Use `os.OpenRoot`, which the kernel enforces:
>
> ```go
> root, err := os.OpenRoot("./static")
> if err != nil {
>     log.Fatal(err)
> }
> app.Mount("/static/", root.FS())
> ```

- [ ] **Step 4: Update the scaffold**

`internal/cli/scaffold/` gains a `static/` directory with a starter stylesheet, and its `main.go.tmpl` mounts it with `os.OpenRoot`, not `os.DirFS`. The scaffold is copied into every new project, so whatever it demonstrates becomes the default everywhere.

Re-run the existing scaffold-compiles test; it must still pass.

- [ ] **Step 5: Update the existing docs**

- `docs/architecture.md`: add `Document` and `Assets` to the component map, and add to "Deviations from the original specification" that the spec described only HTML pages.
- `docs/caching.md`: state that mounted assets never enter the page cache and why.
- `docs/cli.md`: state that `collage build` writes documents to their literal paths and copies mounts unless `WithoutBuildCopy` is set.
- `README.md`: add both to the feature list and the docs map.

- [ ] **Step 6: Full gate and commit**

```bash
gofmt -l . && go build ./... && go vet ./... && go test ./... -count=1 && go test ./... -race -count=1
git add README.md docs/ examples/ internal/cli/
git commit -m "docs: document assets and documents, extend the blog example"
```
