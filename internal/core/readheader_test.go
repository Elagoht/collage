package core

import (
	"net/http"
	"testing"
	"time"
)

// ReadHeaderTimeout bounds the headers alone. Unset, it is ReadTimeout, which
// is what the server always used, so a slow-header client is bounded either way.
func TestHTTPServer_ReadHeaderTimeout(t *testing.T) {
	for _, tc := range []struct {
		name       string
		read, head time.Duration
		want       time.Duration
	}{
		{"unset falls back to ReadTimeout", 15 * time.Second, 0, 15 * time.Second},
		{"set is used", 15 * time.Second, 2 * time.Second, 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp(t, func(c *Config) {
				c.Server.ReadTimeout = tc.read
				c.Server.ReadHeaderTimeout = tc.head
			})
			server := app.httpServer(http.NotFoundHandler())
			if server.ReadHeaderTimeout != tc.want {
				t.Errorf("ReadHeaderTimeout = %v, want %v", server.ReadHeaderTimeout, tc.want)
			}
			if server.ReadTimeout != tc.read {
				t.Errorf("ReadTimeout = %v, want %v", server.ReadTimeout, tc.read)
			}
		})
	}
}
