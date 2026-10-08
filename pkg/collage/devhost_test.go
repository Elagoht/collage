package collage_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// Run directly with DevMode on — COLLAGE_DEV=1 without collage dev — the
// application refuses a Host that does not name this machine, so a page on
// another site that made its name resolve here cannot read the development
// error pages or the reload stream. Server.Host is one of the names allowed.
func TestDevMode_RefusesAForeignHost(t *testing.T) {
	t.Setenv("COLLAGE_DEV_HOST", "")
	for _, tc := range []struct {
		dev   bool
		host  string
		names map[string]int
	}{
		{true, "mybox.local", map[string]int{
			"evil.attacker.io:6060": http.StatusForbidden,
			"localhost:6060":        http.StatusNotFound,
			"127.0.0.1:6060":        http.StatusNotFound,
			"[::1]:6060":            http.StatusNotFound,
			"10.0.0.7:6060":         http.StatusNotFound,
			"mybox.local:6060":      http.StatusNotFound,
		}},
		{true, "0.0.0.0", map[string]int{
			"evil.attacker.io": http.StatusForbidden,
			"mybox.local:6060": http.StatusForbidden,
			"10.0.0.7:6060":    http.StatusNotFound,
		}},
		{false, "localhost", map[string]int{"evil.attacker.io": http.StatusNotFound}},
	} {
		app, err := collage.New(&collage.Config{
			DevMode:  tc.dev,
			Server:   collage.ServerConfig{Host: tc.host, Port: 6060},
			Template: collage.TemplateConfig{FS: fstest.MapFS{"t/keep.html": {Data: []byte(`x`)}}, Root: "t"},
		})
		if err != nil {
			t.Fatal(err)
		}
		h := app.Handler()
		for host, want := range tc.names {
			r := httptest.NewRequest(http.MethodGet, "/missing", nil)
			r.Host = host
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want {
				t.Errorf("dev=%v Server.Host=%q: GET from Host %q = %d, want %d", tc.dev, tc.host, host, w.Code, want)
			}
		}
	}
}

// COLLAGE_DEV_HOST names more hosts, comma-separated: a docker-compose
// service, an /etc/hosts name, a LAN name while bound to 0.0.0.0. A refusal
// says so, and quotes the Host it refused rather than echoing it.
func TestDevMode_ExtraHostsFromTheEnvironment(t *testing.T) {
	t.Setenv("COLLAGE_DEV_HOST", "app, mybox.lan")
	app, err := collage.New(&collage.Config{
		DevMode:  true,
		Server:   collage.ServerConfig{Host: "0.0.0.0", Port: 6060},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/keep.html": {Data: []byte(`x`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := app.Handler()
	for host, want := range map[string]int{
		"app:6060":              http.StatusNotFound,
		"mybox.lan":             http.StatusNotFound,
		"evil.attacker.io:6060": http.StatusForbidden,
	} {
		r := httptest.NewRequest(http.MethodGet, "/missing", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %q = %d, want %d", host, w.Code, want)
		}
		if want == http.StatusForbidden {
			body := w.Body.String()
			if !strings.Contains(body, "COLLAGE_DEV_HOST") || !strings.Contains(body, `"evil.attacker.io:6060"`) {
				t.Errorf("refusal %q does not name COLLAGE_DEV_HOST and the quoted Host", body)
			}
		}
	}
}
