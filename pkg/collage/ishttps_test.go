package collage_test

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

func schemeOf(t *testing.T, trusted []string, remote, proto string, viaTLS bool) bool {
	t.Helper()
	var got bool
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000, TrustedProxies: trusted},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>x</p>`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Handle("/scheme", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = collage.IsHTTPS(r)
	})); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/scheme", nil)
	req.RemoteAddr = remote
	if proto != "" {
		req.Header.Set("X-Forwarded-Proto", proto)
	}
	if viaTLS {
		req.TLS = &tls.ConnectionState{}
	}
	app.Handler().ServeHTTP(httptest.NewRecorder(), req)
	return got
}

// IsHTTPS is the decision the forgery guard's cookie makes: a TLS connection,
// or a proxy's X-Forwarded-Proto believed only from TrustedProxies when any are
// listed.
func TestIsHTTPS(t *testing.T) {
	proxy := []string{"10.0.0.0/8"}
	for _, tt := range []struct {
		name    string
		trusted []string
		remote  string
		proto   string
		tls     bool
		want    bool
	}{
		{"plain", proxy, "10.0.0.1:1234", "", false, false},
		{"tls", proxy, "9.9.9.9:1234", "", true, true},
		{"trusted proxy https", proxy, "10.0.0.1:1234", "https", false, true},
		{"trusted proxy list", proxy, "10.0.0.1:1234", "HTTPS, http", false, true},
		{"trusted proxy http", proxy, "10.0.0.1:1234", "http", false, false},
		{"untrusted peer", proxy, "9.9.9.9:1234", "https", false, false},
		{"long s is not s", proxy, "10.0.0.1:1234", "httpſ", false, false},
		{"no proxies listed believes the header", nil, "9.9.9.9:1234", "https", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := schemeOf(t, tt.trusted, tt.remote, tt.proto, tt.tls); got != tt.want {
				t.Errorf("IsHTTPS = %v, want %v", got, tt.want)
			}
		})
	}
}

// Outside an application, no proxies are known: as for the forgery guard with
// none listed, TLS or the header decides.
func TestIsHTTPS_OutsideAnApp(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if collage.IsHTTPS(r) {
		t.Error("a plain request is HTTPS")
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	if !collage.IsHTTPS(r) {
		t.Error("the header is not believed with no proxies known")
	}
}
