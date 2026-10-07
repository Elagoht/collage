package collage_test

import (
	"errors"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

func TestBuilders_RefuseControlCharactersInRedirects(t *testing.T) {
	page := collage.NewPage("p").WithRedirect("/a\r\nX: y", "/b", 301)
	if err := page.BuildErr(); !errors.Is(err, collage.ErrInvalidRedirect) {
		t.Errorf("page WithRedirect: %v, want ErrInvalidRedirect", err)
	}
	permanent := collage.NewPage("p").WithPermanentRedirect("/a", "/b\n")
	if err := permanent.BuildErr(); !errors.Is(err, collage.ErrInvalidRedirect) {
		t.Errorf("page WithPermanentRedirect: %v, want ErrInvalidRedirect", err)
	}
	doc := collage.NewDocument("d", "text/plain").WithRedirect("/a", "/b\r", 302)
	if err := doc.BuildErr(); !errors.Is(err, collage.ErrInvalidRedirect) {
		t.Errorf("document WithRedirect: %v, want ErrInvalidRedirect", err)
	}
	if err := collage.NewPage("p").WithRedirect("/a", "/b", 301).BuildErr(); err != nil {
		t.Errorf("a clean redirect: %v", err)
	}
	docPermanent := collage.NewDocument("d", "text/plain").WithPermanentRedirect("/a\n", "/b")
	if err := docPermanent.BuildErr(); !errors.Is(err, collage.ErrInvalidRedirect) {
		t.Errorf("document WithPermanentRedirect: %v, want ErrInvalidRedirect", err)
	}
}

func TestValidate_RefusesControlCharactersInLiteralRedirects(t *testing.T) {
	page := &collage.Page{Name: "p", ContentFragment: &collage.Fragment{Name: "c"}, Redirects: []*collage.Redirect{{From: "/a\n", To: "/b"}}}
	if err := page.Validate(); !errors.Is(err, collage.ErrInvalidRedirect) {
		t.Errorf("page literal: %v, want ErrInvalidRedirect", err)
	}
	doc := &collage.Document{Name: "d", ContentType: "text/plain", Body: []byte("x"), Redirects: []*collage.Redirect{{From: "/a", To: "/b\r"}}}
	if err := doc.Validate(); !errors.Is(err, collage.ErrInvalidRedirect) {
		t.Errorf("document literal: %v, want ErrInvalidRedirect", err)
	}
}
