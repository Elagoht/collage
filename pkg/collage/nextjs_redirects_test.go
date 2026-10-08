package collage_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// Next.js custom-routes: "should allow params in query for redirect" — the
// request's query string is carried to the destination.
func TestNextJS_RedirectCarriesTheQuery(t *testing.T) {
	h := nextSite(t, false, nil, func(app *collage.App) {
		p := collage.NewPage("with-params").WithContent(collage.NewFragment("wp-content", "p.html").Build()).
			WithPath("en", "/with-params").WithRedirect("/query-redirect", "/with-params", 307).Build()
		mustRegister(t, app, p)
	})
	w := do(h, http.MethodGet, "/query-redirect?a=b", nil)
	if w.Code != 307 || w.Header().Get("Location") != "/with-params?a=b" {
		t.Errorf("got %d %q, want 307 /with-params?a=b", w.Code, w.Header().Get("Location"))
	}
}

// Next.js custom-routes: "should have correctly encoded params in query for
// redirect" — a captured value substituted into the destination's query string
// is one value, not the start of another parameter.
func TestNextJS_RedirectParamInQueryIsOneValue(t *testing.T) {
	h := nextSite(t, false, nil, func(app *collage.App) {
		p := collage.NewPage("with-params").WithContent(collage.NewFragment("wp-content", "p.html").Build()).
			WithPath("en", "/with-params").
			WithRedirect("/query-redirect/{first}/{second}", "/with-params?first={first}&second={second}", 307).Build()
		mustRegister(t, app, p)
	})
	w := do(h, http.MethodGet, "/query-redirect/hello%20world%3Fw%3D24%26focalpoint%3Dcenter/world", nil)
	target, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := target.Query()
	if w.Code != 307 || target.Path != "/with-params" || query.Get("first") != "hello world?w=24&focalpoint=center" || len(query) != 2 {
		t.Errorf("got %d %q: query %v, want first=%q second=world only", w.Code, w.Header().Get("Location"), query, "hello world?w=24&focalpoint=center")
	}
}

// Next.js vary-header: "should preserve middleware vary header in combination with
// route handlers" — a Vary middleware wrote is kept beside the one the framework
// adds for what the request declared.
func TestNextJS_VaryKeepsMiddlewares(t *testing.T) {
	h := nextSite(t, false, func(cfg *collage.Config) {
		cfg.Cache = collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Minute}
	}, func(app *collage.App) {
		home := collage.NewPage("home").WithContent(collage.NewFragment("home-content", "p.html").Build()).
			WithPath("en", "/").Incremental(time.Minute).Build()
		mustRegister(t, app, home)
		if err := app.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("My-Custom-Header", "test")
				w.Header().Add("Vary", "My-Custom-Header")
				if err := collage.Vary(r, "Accept-Language", "en"); err != nil {
					t.Error(err)
				}
				next.ServeHTTP(w, r)
			})
		}); err != nil {
			t.Fatal(err)
		}
	})
	for i := range 2 { // a render, then the cached copy
		w := do(h, http.MethodGet, "/", nil)
		vary := strings.Join(w.Header().Values("Vary"), ", ")
		if !strings.Contains(vary, "My-Custom-Header") || !strings.Contains(vary, "Accept-Language") {
			t.Errorf("request %d: Vary = %q, want both My-Custom-Header and Accept-Language", i+1, vary)
		}
	}
}

// Next.js x-forwarded-headers, with the proxy in front reporting https as a list:
// the forgery cookie on a response the reader got over TLS is Secure.
func TestNextJS_ListValuedForwardedProtoIsSecure(t *testing.T) {
	h := nextSite(t, true, nil, func(app *collage.App) {
		mustRegister(t, app, collage.NewPage("form").WithContent(collage.NewFragment("form-content", "form.html").Build()).WithPath("en", "/").Build())
	})
	for _, proto := range []string{"https", "https, https", "https,http"} {
		w := do(h, http.MethodGet, "/", http.Header{"X-Forwarded-Proto": {proto}})
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || !cookies[0].Secure {
			t.Errorf("X-Forwarded-Proto %q: cookies %v, want one Secure", proto, w.Header().Values("Set-Cookie"))
		}
	}
}

// Behind TrustedProxies, X-Forwarded-Proto is believed only from them: a client
// that reaches the server directly cannot claim TLS it does not have.
func TestForwardedProto_BelievedOnlyFromTrustedProxies(t *testing.T) {
	h := nextSite(t, true, func(cfg *collage.Config) {
		cfg.Server.TrustedProxies = []string{"10.0.0.1"}
	}, func(app *collage.App) {
		mustRegister(t, app, collage.NewPage("form").WithContent(collage.NewFragment("form-content", "form.html").Build()).WithPath("en", "/").Build())
	})
	for remote, secure := range map[string]bool{"10.0.0.1:5000": true, "203.0.113.9:5000": false} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Secure != secure {
			t.Errorf("from %s: cookies %v, want one with Secure=%v", remote, w.Header().Values("Set-Cookie"), secure)
		}
	}
}
