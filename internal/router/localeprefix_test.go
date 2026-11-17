package router

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// resolveLocale strips a leading segment that names a supported locale, so a route
// whose own first segment is one registered fine and could never be reached:
// /tr/guide.txt in the en tree is a request for /guide.txt in tr, app.URL built a
// dead link to it, and a redirect from /tr/eski fired only at /tr/tr/eski. Each is
// refused where it is registered, naming the locale.
func TestRegister_RefusesAFirstSegmentThatIsASupportedLocale(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr"}}
	for _, pattern := range []string{"/tr/guide.txt", "/TR/guide.txt", "/en/self.txt", "/tr"} {
		if err := New(opts).RegisterDocument(testDocument("guide", pattern)); !errors.Is(err, types.ErrLocaleUnreachable) {
			t.Errorf("document at %q = %v, want ErrLocaleUnreachable", pattern, err)
		}
		if err := New(opts).Register(newTestPage("p", map[string]string{"en": pattern})); !errors.Is(err, types.ErrLocaleUnreachable) {
			t.Errorf("page at %q = %v, want ErrLocaleUnreachable", pattern, err)
		}
		if err := New(opts).RegisterAction(testAction("a", pattern, http.MethodPost)); !errors.Is(err, types.ErrLocaleUnreachable) {
			t.Errorf("action at %q = %v, want ErrLocaleUnreachable", pattern, err)
		}
		redirected := newTestPageWithRedirects("r", map[string]string{"en": "/r"}, []*types.Redirect{{From: pattern, To: "/r"}})
		err := New(opts).Register(redirected)
		if !errors.Is(err, types.ErrLocaleUnreachable) || !strings.Contains(err.Error(), pattern) {
			t.Errorf("redirect from %q = %v, want ErrLocaleUnreachable naming it", pattern, err)
		}
	}
	// Only the first segment is the prefix, and only a literal one: these stay.
	for _, pattern := range []string{"/guide/tr", "/{lang}/guide", "/tr-guide", "/{x}.txt", "/trx"} {
		if err := New(opts).RegisterDocument(testDocument("guide", pattern)); err != nil {
			t.Errorf("document at %q = %v, want it registered", pattern, err)
		}
	}
}
