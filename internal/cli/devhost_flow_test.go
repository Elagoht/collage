package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/internal/devhost"
	"github.com/Elagoht/collage/pkg/collage"
)

// The program behind collage dev applies the proxy's own Host rule. It listens
// on loopback but sees the browser's Host, the proxy's, so collage dev tells it
// that host: every Host the proxy lets through, the program answers too —
// including a name the proxy was started with that is neither localhost nor an
// address.
func TestDevProxy_TheProgramAllowsWhatTheProxyAllows(t *testing.T) {
	t.Setenv(devhost.EnvHost, "dev.example.test")
	app, err := collage.New(&collage.Config{
		DevMode:  true,
		Server:   collage.ServerConfig{Host: "127.0.0.1", Port: 0},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/keep.html": {Data: []byte(`x`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	program := httptest.NewServer(app.Handler())
	defer program.Close()
	target, err := url.Parse(program.URL)
	if err != nil {
		t.Fatal(err)
	}

	p := newDevProxy("dev.example.test:6060", target.Host)
	p.set(devReady, nil, "")
	for host, allowed := range map[string]bool{
		"localhost:6060":            true,
		"app.localhost:6060":        true,
		"127.0.0.1:6060":            true,
		"[::1]:6060":                true,
		"192.168.1.20:6060":         true,
		"dev.example.test:6060":     true,
		"rebind.attacker.test:6060": false,
	} {
		r := httptest.NewRequest(http.MethodGet, "/_collage/reload-worker.js", nil)
		r.Host = host
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if allowed && w.Code != http.StatusOK {
			t.Errorf("Host %q through the proxy: %d %q, want the program's 200", host, w.Code, w.Body.String())
		}
		if !allowed && w.Code != http.StatusForbidden {
			t.Errorf("Host %q through the proxy: %d, want 403", host, w.Code)
		}
	}
}
