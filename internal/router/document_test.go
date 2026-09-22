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
	rt := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
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
	rt := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	if err := rt.Register(newTestPage("post", map[string]string{"en": "/sitemap.xml"})); err != nil {
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
	rt := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	if err := rt.RegisterDocument(testDocument("sitemap", "/sitemap.xml")); err != nil {
		t.Fatalf("RegisterDocument() = %v", err)
	}

	err := rt.Register(newTestPage("post", map[string]string{"en": "/sitemap.xml"}))
	if !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("Register() = %v, want ErrDuplicateRoute — the collision check must be symmetric", err)
	}
}

func TestRegisterDocument_StaticBeatsADynamicPage(t *testing.T) {
	rt := New(LocaleOptions{Default: "en", Supported: []string{"en"}})
	if err := rt.Register(newTestPage("post", map[string]string{"en": "/{slug}"})); err != nil {
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
	rt := New(LocaleOptions{Default: "en", Supported: []string{"en", "tr"}})
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
