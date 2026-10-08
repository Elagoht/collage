package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// In development, a Host that does not name this machine is a page on another
// site that made its name resolve here: refused before anything is served — a
// page, the reload stream, the worker — and with nothing of the application in
// the answer.
func TestDevHost_RefusesAForeignHost(t *testing.T) {
	page := testPage("home", "/", types.StrategyDynamic)
	env := newEnv(t, []*types.Page{page}, withDevMode(), func(d *Deps) {
		d.DevHosts = []string{"mybox.local", ""}
	})
	request := func(host, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Host = host
		return env.do(r)
	}
	for _, path := range []string{"/", "/missing", devReloadPath, devReloadWorkerPath} {
		rec := request("evil.example:6060", path)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s from Host evil.example = %d, want 403", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "page") || strings.Contains(rec.Body.String(), devReloadPath) {
			t.Errorf("GET %s from Host evil.example served %q", path, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("GET %s refusal Cache-Control = %q, want no-store", path, rec.Header().Get("Cache-Control"))
		}
	}
	for _, host := range []string{
		"localhost", "localhost:6060", "app.localhost:6060", "127.0.0.1:6060", "[::1]:6060",
		"192.168.1.20:6060", "mybox.local:6060", "MYBOX.local.",
	} {
		if rec := request(host, "/"); rec.Code != http.StatusOK {
			t.Errorf("GET / from Host %q = %d, want 200", host, rec.Code)
		}
	}
	if rec := request("", "/"); rec.Code != http.StatusForbidden {
		t.Errorf("GET / with no Host = %d, want 403", rec.Code)
	}
}

// In production the Host is the application's business, not the framework's: a
// site is reached by whatever names its DNS gives it.
func TestDevHost_NotCheckedInProduction(t *testing.T) {
	page := testPage("home", "/", types.StrategyDynamic)
	env := newEnv(t, []*types.Page{page})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "evil.example"
	if rec := env.do(r); rec.Code != http.StatusOK {
		t.Errorf("production GET / from any Host = %d, want 200", rec.Code)
	}
}
