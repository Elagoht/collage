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
}
