package router

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// A Turkish-only site registered under "tr" with the locale config left out: the
// default is "en", nothing reaches "tr", and every page used to answer 404.
func TestRegister_PathInUnreachableLocale_Rejected(t *testing.T) {
	cases := []struct {
		name string
		opts LocaleOptions
		want []string
	}{
		{"not supported", LocaleOptions{Default: "en", Supported: []string{"en"}}, []string{`"tr"`, `default locale is "en"`, "[en]"}},
		{"path locale off", LocaleOptions{Default: "en", Supported: []string{"en", "tr"}, DisablePathLocale: true}, []string{`"tr"`, "DisablePathLocale"}},
	}
	register := map[string]func(Router) error{
		"page": func(rt Router) error {
			return rt.Register(&types.Page{Name: "home", Paths: map[string]string{"tr": "/"}})
		},
		"action": func(rt Router) error {
			return rt.RegisterAction(&types.Action{Name: "login", Paths: map[string]string{"tr": "/giris"}, Methods: []string{http.MethodPost},
				Handler: func(context.Context, *types.RenderContext) (*types.ActionResult, error) { return nil, nil }})
		},
		"document": func(rt Router) error {
			return rt.RegisterDocument(&types.Document{Name: "feed", ContentType: "application/xml", Paths: map[string]string{"tr": "/akis.xml"}})
		},
	}
	for _, c := range cases {
		for kind, reg := range register {
			t.Run(c.name+"/"+kind, func(t *testing.T) {
				err := reg(New(c.opts))
				if !errors.Is(err, types.ErrLocaleUnreachable) {
					t.Fatalf("err = %v, want ErrLocaleUnreachable", err)
				}
				for _, w := range append([]string{kind}, c.want...) {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("err = %q, want it to mention %s", err, w)
					}
				}
			})
		}
	}
}

func TestRegister_PathInReachableLocale_Accepted(t *testing.T) {
	rt := New(LocaleOptions{Default: "tr", Supported: []string{"tr", "en"}})
	mustRegister(t, rt, &types.Page{Name: "home", Paths: map[string]string{"tr": "/", "en": "/"}})
	// Only in a locale besides the default: reached at /en/about.
	mustRegister(t, rt, &types.Page{Name: "about", Paths: map[string]string{"en": "/about"}})
	// Outside every locale.
	if err := rt.RegisterDocument(&types.Document{Name: "robots", ContentType: "text/plain", Paths: map[string]string{types.RootLocale: "/robots.txt"}}); err != nil {
		t.Fatalf("RegisterDocument(root) = %v", err)
	}
}
