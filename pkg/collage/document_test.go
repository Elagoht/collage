package collage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestNewDocument_BuildsTheSpecShape checks that every DocumentBuilder method sets
// the field it documents, mirroring TestNewPage_BuildsTheSpecShape for pages.
func TestNewDocument_BuildsTheSpecShape(t *testing.T) {
	doc := NewDocument("sitemap", "application/xml").
		WithPath("en", "/sitemap.xml").
		WithHandler(func(ctx context.Context, rc *RenderContext) ([]byte, []string, error) {
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
	if doc.Strategy != StrategyIncremental || doc.CacheTTL != time.Hour {
		t.Fatalf("strategy = %v ttl = %v", doc.Strategy, doc.CacheTTL)
	}
	if len(doc.DependencyTags) != 1 || doc.DependencyTags[0] != "site" {
		t.Fatalf("DependencyTags = %v, want [site]", doc.DependencyTags)
	}
	if doc.Handler == nil {
		t.Fatal("Handler = nil, want the function passed to WithHandler")
	}
}

// TestDocumentBuilder_BuildErrReportsAMissingHandler checks that a document built
// without WithHandler records ErrNoDocumentHandler rather than panicking or being
// silently accepted — Build never panics and never returns nil, but BuildErr must
// surface the mistake.
func TestDocumentBuilder_BuildErrReportsAMissingHandler(t *testing.T) {
	b := NewDocument("sitemap", "application/xml").WithPath("en", "/sitemap.xml")
	b.Build()
	if !errors.Is(b.BuildErr(), ErrNoDocumentHandler) {
		t.Fatalf("BuildErr() = %v, want ErrNoDocumentHandler", b.BuildErr())
	}
}
