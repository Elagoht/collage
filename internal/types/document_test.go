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
		{"negative ttl with incremental strategy reports the negative first", func(d *Document) {
			d.CacheTTL = -time.Second
			d.Strategy = StrategyIncremental
		}, ErrInvalidTTL},
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
