package collage_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

// getPage builds an app with one plain page at "/" (tweak adjusts the Config
// first) and returns the response to a GET of it.
func getPage(t *testing.T, tweak func(*collage.Config)) *http.Response {
	t.Helper()
	handler := nextSite(t, false, tweak, func(app *collage.App) {
		mustRegister(t, app, plainPage("home", "/"))
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec.Result()
}

// TestBaselineSecurityHeadersDefault: a page is answered with the baseline
// security headers on, without the application asking for them.
func TestBaselineSecurityHeadersDefault(t *testing.T) {
	resp := getPage(t, nil)
	if got := resp.StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want %q", got, "nosniff")
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q, want %q", got, "SAMEORIGIN")
	}
}

// TestBaselineSecurityHeadersOmitted: "-" for FrameOptions and a false NoSniff
// send neither header, for an application that has its own policy and does not
// want the framework's.
func TestBaselineSecurityHeadersOmitted(t *testing.T) {
	off := false
	resp := getPage(t, func(cfg *collage.Config) {
		cfg.Security.FrameOptions = "-"
		cfg.Security.NoSniff = &off
	})
	if got := resp.Header.Get("X-Frame-Options"); got != "" {
		t.Errorf("X-Frame-Options = %q, want it unset", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "" {
		t.Errorf("X-Content-Type-Options = %q, want it unset", got)
	}
}

// TestBaselineSecurityHeadersCustom: a FrameOptions value other than "" or "-"
// is sent verbatim.
func TestBaselineSecurityHeadersCustom(t *testing.T) {
	resp := getPage(t, func(cfg *collage.Config) {
		cfg.Security.FrameOptions = "DENY"
	})
	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want %q", got, "DENY")
	}
}

// TestBaselineSecurityHeadersOverridable: middleware — which is where a plugin
// such as elagoht/secure sets its headers — runs after the baseline is written
// and its Header().Set wins. The baseline is a floor, not a ceiling.
func TestBaselineSecurityHeadersOverridable(t *testing.T) {
	handler := nextSite(t, false, nil, func(app *collage.App) {
		mustRegister(t, app, plainPage("home", "/"))
		if err := app.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Frame-Options", "DENY")
				w.Header().Set("Content-Security-Policy", "default-src 'self'")
				next.ServeHTTP(w, r)
			})
		}); err != nil {
			t.Fatalf("Use: %v", err)
		}
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	resp := rec.Result()

	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want middleware's %q to win over the baseline", got, "DENY")
	}
	if got := resp.Header.Get("Content-Security-Policy"); got != "default-src 'self'" {
		t.Errorf("Content-Security-Policy = %q, want the middleware's", got)
	}
	// The baseline nosniff the middleware did not touch is still there.
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want %q untouched", got, "nosniff")
	}
}
