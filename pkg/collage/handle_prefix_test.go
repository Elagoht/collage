package collage_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

// The prefix check refused anything containing "/.", which also refused
// "/.well-known/...", the one place a site must mount a handler (ACME challenges,
// security.txt, passkey and app-association files). Only a dot segment is a
// reason, since the clean-path redirect never lets one reach the handler.
func TestHandle_DotfilePrefixesAreAccepted(t *testing.T) {
	for _, prefix := range []string{"/.well-known/acme-challenge/", "/.well-known/security.txt", "/a/.hidden/", "/v1.2/", "/a/..b/"} {
		if err := langApp(t, false, nil).Handle(prefix, http.NotFoundHandler()); err != nil {
			t.Errorf("Handle(%q) = %v, want it accepted", prefix, err)
		}
	}
}

func TestHandle_WellKnownIsServed(t *testing.T) {
	app := langApp(t, false, nil)
	hit := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("challenge")) })
	if err := app.Handle("/.well-known/acme-challenge/", hit); err != nil {
		t.Fatal(err)
	}
	if err := app.Handle("/.well-known/security.txt", hit); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/.well-known/acme-challenge/token", "/.well-known/security.txt"} {
		rec := request(app.Handler(), http.MethodGet, path)
		if rec.Code != http.StatusOK || rec.Body.String() != "challenge" {
			t.Errorf("GET %s = %d %q, want the handler's answer", path, rec.Code, rec.Body.String())
		}
	}
}

func TestHandle_RefusalsNameTheirReason(t *testing.T) {
	cases := map[string]string{
		"/a/./b/":  "dot segment",
		"/a/../b/": "dot segment",
		"/..":      "dot segment",
		"/a//b/":   `"//"`,
		"//a/":     `"//"`,
		"/":        `not be "/"`,
		"a/":       `begin with "/"`,
		"":         `begin with "/"`,
	}
	for prefix, reason := range cases {
		err := langApp(t, false, nil).Handle(prefix, http.NotFoundHandler())
		if !errors.Is(err, collage.ErrInvalidPrefix) {
			t.Errorf("Handle(%q) = %v, want ErrInvalidPrefix", prefix, err)
			continue
		}
		if !strings.Contains(err.Error(), reason) {
			t.Errorf("Handle(%q) = %q, want it to say %s", prefix, err, reason)
		}
	}
}
