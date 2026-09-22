package core

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

// newSitemapDocument returns a minimal, valid document fixture: an XML sitemap
// with one path, dynamic strategy, and a handler that returns fixed bytes and one
// dependency tag. Tests that need different behaviour mutate the fields they care
// about — the same convention newHomePage establishes for pages.
func newSitemapDocument() *types.Document {
	return &types.Document{
		Name:        "sitemap",
		ContentType: "application/xml",
		Paths:       map[string]string{"en": "/sitemap.xml"},
		Handler: func(context.Context, *types.RenderContext) ([]byte, []string, error) {
			return []byte("<urlset/>"), []string{"blog:posts"}, nil
		},
	}
}

// TestRegisterDocument_RejectsADuplicateName: two documents cannot share a name,
// since the name is how Documents and any future by-name lookup reaches one.
func TestRegisterDocument_RejectsADuplicateName(t *testing.T) {
	app := newTestApp(t, nil)
	if err := app.RegisterDocument(newSitemapDocument()); err != nil {
		t.Fatalf("RegisterDocument: %v", err)
	}

	duplicate := newSitemapDocument()
	duplicate.Paths = map[string]string{"en": "/other-sitemap.xml"}

	err := app.RegisterDocument(duplicate)
	if !errors.Is(err, ErrDuplicateDocument) {
		t.Fatalf("RegisterDocument = %v, want ErrDuplicateDocument", err)
	}
	if !strings.Contains(err.Error(), "sitemap") {
		t.Fatalf("error %q does not name the document", err)
	}
}

// TestRegisterDocument_RejectsAfterStart: registration closes the moment the
// handler is built, exactly as it does for pages.
func TestRegisterDocument_RejectsAfterStart(t *testing.T) {
	app := newTestApp(t, nil)
	app.Handler()

	if err := app.RegisterDocument(newSitemapDocument()); !errors.Is(err, ErrAppStarted) {
		t.Fatalf("RegisterDocument after start = %v, want ErrAppStarted", err)
	}
}

// TestRegisterDocument_RejectsAnInvalidDocument: types.Document.Validate's own
// failure modes surface through RegisterDocument with their sentinel intact, named
// against the document.
func TestRegisterDocument_RejectsAnInvalidDocument(t *testing.T) {
	app := newTestApp(t, nil)
	doc := newSitemapDocument()
	doc.ContentType = ""

	err := app.RegisterDocument(doc)
	if !errors.Is(err, types.ErrEmptyContentType) {
		t.Fatalf("RegisterDocument = %v, want ErrEmptyContentType", err)
	}
	if !strings.Contains(err.Error(), "sitemap") {
		t.Fatalf("error %q does not name the document", err)
	}
}

// TestApp_ServesADocumentEndToEnd proves a document is reachable over HTTP with
// exactly the content type and bytes its handler produced, and that it carries an
// ETag the way a cacheable response must.
func TestApp_ServesADocumentEndToEnd(t *testing.T) {
	app := newTestApp(t, nil)
	if err := app.RegisterDocument(newSitemapDocument()); err != nil {
		t.Fatalf("RegisterDocument: %v", err)
	}

	recorder := get(app.Handler(), "/sitemap.xml")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/xml" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/xml")
	}
	if got := recorder.Body.String(); got != "<urlset/>" {
		t.Fatalf("body = %q, want %q", got, "<urlset/>")
	}
	if recorder.Header().Get("ETag") == "" {
		t.Fatal("ETag is empty, want non-empty")
	}
}

// TestApp_InvalidateTagsRegeneratesADocument is the test that matters most: a
// status code cannot distinguish a cache hit from a re-execution, so this counts
// how many times the handler itself ran. Two requests must run it once; after
// invalidating its tag, a third request must run it again.
func TestApp_InvalidateTagsRegeneratesADocument(t *testing.T) {
	app := newTestApp(t, nil)

	calls := 0
	doc := newSitemapDocument()
	doc.Strategy = types.StrategyIncremental
	doc.CacheTTL = time.Minute
	doc.Handler = func(context.Context, *types.RenderContext) ([]byte, []string, error) {
		calls++
		return []byte("<urlset/>"), []string{"blog:posts"}, nil
	}

	if err := app.RegisterDocument(doc); err != nil {
		t.Fatalf("RegisterDocument: %v", err)
	}

	handler := app.Handler()

	get(handler, "/sitemap.xml")
	get(handler, "/sitemap.xml")
	if calls != 1 {
		t.Fatalf("handler ran %d times after two requests, want 1 (the second must be served from cache)", calls)
	}

	if err := app.InvalidateTags(context.Background(), "blog:posts"); err != nil {
		t.Fatalf("InvalidateTags: %v", err)
	}

	get(handler, "/sitemap.xml")
	if calls != 2 {
		t.Fatalf("handler ran %d times after invalidation, want 2 (the third request must re-execute)", calls)
	}
}

// TestRenderDocumentPath_UsesTheRoutersLocale checks the same rule already ruled
// for RenderPath: a caller-supplied locale that disagrees with the matched route
// must never be silently honoured. Either the handler observes the router's
// resolved locale (tr), or the call errors instead of executing the handler with
// the caller's disagreeing "en".
func TestRenderDocumentPath_UsesTheRoutersLocale(t *testing.T) {
	app := newTestApp(t, func(cfg *Config) {
		cfg.Locale.Supported = []string{"en", "tr"}
	})

	observedLocale := ""
	doc := newSitemapDocument()
	doc.Name = "site-haritasi"
	doc.Paths = map[string]string{"tr": "/site-haritasi.xml"}
	doc.Handler = func(_ context.Context, rc *types.RenderContext) ([]byte, []string, error) {
		observedLocale = rc.Locale
		return []byte("<urlset/>"), nil, nil
	}

	if err := app.RegisterDocument(doc); err != nil {
		t.Fatalf("RegisterDocument: %v", err)
	}

	result, err := app.RenderDocumentPath(context.Background(), "/tr/site-haritasi.xml", "en", nil)
	if err != nil {
		// Refusing the disagreement outright is an acceptable alternative to
		// rendering under the router's locale; either way the tr document must
		// never be rendered while telling the handler "en".
		return
	}
	if result == nil {
		t.Fatal("RenderDocumentPath returned a nil result and a nil error")
	}
	if observedLocale != "tr" {
		t.Fatalf("handler observed locale = %q, want tr: the tr document rendered as %q instead", observedLocale, observedLocale)
	}
}
