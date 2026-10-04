package collage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// stamp replaces "MARK" with a number new on every response, as a nonce would.
type stamp struct {
	n        atomic.Int32
	personal bool
	fail     error
	panics   bool
}

func (p *stamp) Name() string                             { return "test/stamp" }
func (p *stamp) Version() string                          { return "0" }
func (p *stamp) Init(context.Context, collage.Host) error { return nil }
func (p *stamp) Shutdown(context.Context) error           { return nil }
func (p *stamp) OnPersonalise(_ context.Context, ev *collage.PersonaliseEvent) error {
	if p.panics {
		panic("boom")
	}
	if p.fail != nil {
		return p.fail
	}
	if !bytes.Contains(ev.Body, []byte("MARK")) {
		return nil
	}
	n := strconv.Itoa(int(p.n.Add(1)))
	ev.Body = bytes.ReplaceAll(ev.Body, []byte("MARK"), []byte("n"+n))
	ev.Header.Set("X-Stamp", n)
	ev.Personal = p.personal
	return nil
}

func stampSite(t *testing.T, p *stamp) http.Handler {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/p.html":  {Data: []byte(`<p>MARK</p>`)},
			"t/nf.html": {Data: []byte(`<p>missing MARK</p>`)},
		}, Root: "t"},
		Cache:   collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Plugins: []collage.Plugin{p},
	})
	if err != nil {
		t.Fatal(err)
	}
	frag := collage.NewFragment("p", "p.html").Build()
	if err := app.RegisterPage(collage.NewPage("home").WithContent(frag).WithPath("en", "/").
		WithFragmentPath("en", "/live", frag).Incremental(time.Hour).Build()); err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterNotFoundPage(collage.NewPage("nf").WithContent(collage.NewFragment("nf", "nf.html").Build()).Build()); err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	return app.Handler()
}

func getStamp(h http.Handler, path, ifNoneMatch string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A cached page goes through the hook on every response, fresh render and cache
// hit alike; made personal, it is private and no-store, and an earlier ETag never
// earns a 304 for a body that changed.
func TestPersonalise_CachedPageIsPersonal(t *testing.T) {
	h := stampSite(t, &stamp{personal: true})
	first := getStamp(h, "/", "")
	second := getStamp(h, "/", first.Header().Get("ETag"))
	if !strings.Contains(first.Body.String(), "<p>n1</p>") || !strings.Contains(second.Body.String(), "<p>n2</p>") {
		t.Fatalf("bodies %q, %q: want n1 then n2", first.Body, second.Body)
	}
	if second.Code != http.StatusOK {
		t.Errorf("second status = %d, want 200 (the body changed)", second.Code)
	}
	for _, rec := range []*httptest.ResponseRecorder{first, second} {
		if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
			t.Errorf("Cache-Control = %q, want private, no-store", cc)
		}
		if rec.Header().Get("X-Stamp") == "" {
			t.Errorf("the hook's header is missing")
		}
	}
	if first.Header().Get("ETag") == second.Header().Get("ETag") {
		t.Errorf("both responses carry ETag %q", first.Header().Get("ETag"))
	}
}

// A hook that rewrites without marking the body personal still gets an ETag that
// names what it sent.
func TestPersonalise_ChangedBodyGetsItsOwnETag(t *testing.T) {
	h := stampSite(t, &stamp{personal: false})
	first := getStamp(h, "/", "")
	second := getStamp(h, "/", first.Header().Get("ETag"))
	if second.Code != http.StatusOK || first.Header().Get("ETag") == second.Header().Get("ETag") {
		t.Errorf("second = %d with ETag %q (first %q): want 200 and a new ETag",
			second.Code, second.Header().Get("ETag"), first.Header().Get("ETag"))
	}
}

// A fragment path and the not-found page go through the hook too.
func TestPersonalise_FragmentAndErrorPage(t *testing.T) {
	h := stampSite(t, &stamp{personal: true})
	if body := getStamp(h, "/live", "").Body.String(); !strings.Contains(body, "<p>n") || strings.Contains(body, "MARK") {
		t.Errorf("fragment path = %q, want it stamped", body)
	}
	nf := getStamp(h, "/nowhere", "")
	if nf.Code != http.StatusNotFound || !strings.Contains(nf.Body.String(), "missing n") {
		t.Errorf("not found = %d %q, want 404 stamped", nf.Code, nf.Body)
	}
}

// A failing hook is a 500 for a page; on the error page it would recurse into,
// the built-in page is written instead, carrying no marker.
func TestPersonalise_HookErrorIs500(t *testing.T) {
	for name, p := range map[string]*stamp{
		"error": {fail: errors.New("no randomness")},
		"panic": {panics: true},
	} {
		t.Run(name, func(t *testing.T) {
			h := stampSite(t, p)
			rec := getStamp(h, "/", "")
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if strings.Contains(rec.Body.String(), "MARK") {
				t.Errorf("body carries the marker: %q", rec.Body)
			}
			nf := getStamp(h, "/nowhere", "")
			if nf.Code != http.StatusNotFound || strings.Contains(nf.Body.String(), "MARK") {
				t.Errorf("not found = %d %q, want the built-in 404 without the marker", nf.Code, nf.Body)
			}
		})
	}
}
