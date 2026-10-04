package collage_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// ipApp is an application whose /ip handler answers with collage.ClientIP.
func ipApp(t *testing.T, trusted []string) http.Handler {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000, TrustedProxies: trusted},
		Template: collage.TemplateConfig{
			FS:   fstest.MapFS{"templates/pages/home.html": {Data: []byte(`<p>home</p>`)}},
			Root: "templates",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.Handle("/ip", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, collage.ClientIP(r).String())
	})); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	return app.Handler()
}

func getIP(h http.Handler) string {
	r := httptest.NewRequest(http.MethodGet, "/ip", nil)
	r.RemoteAddr = "10.0.0.1:1"
	r.Header.Set("X-Forwarded-For", "9.9.9.9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Body.String()
}

func TestClientIP_BehindATrustedProxy(t *testing.T) {
	if got := getIP(ipApp(t, []string{"10.0.0.0/8"})); got != "9.9.9.9" {
		t.Errorf("ClientIP = %q, want the forwarded 9.9.9.9", got)
	}
}

func TestClientIP_NoTrustedProxies(t *testing.T) {
	if got := getIP(ipApp(t, nil)); got != "10.0.0.1" {
		t.Errorf("ClientIP = %q, want RemoteAddr's 10.0.0.1: no header is trusted", got)
	}
}

func TestClientIP_ABadTrustedProxyFailsNew(t *testing.T) {
	_, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000, TrustedProxies: []string{"nope"}},
		Template: collage.TemplateConfig{FS: fstest.MapFS{}, Root: "templates"},
	})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("New() = %v, want an error naming \"nope\"", err)
	}
}

func TestClientIP_TrustingEveryAddressIsWarned(t *testing.T) {
	for _, tt := range []struct {
		trusted []string
		warns   int
	}{
		{[]string{"0.0.0.0/0"}, 1},
		{[]string{"::/0"}, 1},
		{[]string{"10.0.0.0/8", "0.0.0.0/0", "::/0"}, 1},
		{[]string{"10.0.0.0/8", "127.0.0.1"}, 0},
		{nil, 0},
	} {
		var buf bytes.Buffer
		_, err := collage.New(&collage.Config{
			Logger: slog.New(slog.NewTextHandler(&buf, nil)),
			Server: collage.ServerConfig{Host: "localhost", Port: 3000, TrustedProxies: tt.trusted},
			Template: collage.TemplateConfig{
				FS:   fstest.MapFS{"templates/pages/home.html": {Data: []byte(`<p>home</p>`)}},
				Root: "templates",
			},
		})
		if err != nil {
			t.Fatalf("New(%v): %v", tt.trusted, err)
		}
		got := strings.Count(buf.String(), "any client name any address")
		if got != tt.warns {
			t.Errorf("TrustedProxies %v: %d warnings, want %d; log:\n%s", tt.trusted, got, tt.warns, buf.String())
		}
		if tt.warns > 0 && !strings.Contains(buf.String(), "level=WARN") {
			t.Errorf("TrustedProxies %v: the warning is not at Warn; log:\n%s", tt.trusted, buf.String())
		}
	}
}
