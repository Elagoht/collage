package collage_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// hostOrigins resolves hosts from a map, and records what its Host told it.
type hostOrigins struct {
	origins      map[string]string
	dynamic      bool
	sawOrigins   bool
	initOriginOf string
}

func (p *hostOrigins) Name() string                   { return "test/origins" }
func (p *hostOrigins) Version() string                { return "0" }
func (p *hostOrigins) Shutdown(context.Context) error { return nil }
func (p *hostOrigins) Init(ctx context.Context, host collage.Host) error {
	if origins, ok := host.(collage.Origins); ok {
		p.sawOrigins = true
		p.dynamic = origins.Dynamic()
		p.initOriginOf = origins.OriginFor(ctx, "acme.test")
	}
	return nil
}
func (p *hostOrigins) Origin(_ context.Context, host string) (string, bool) {
	origin, ok := p.origins[host]
	return origin, ok
}

func originSite(t *testing.T, dev bool, logs *bytes.Buffer, p *hostOrigins) *collage.App {
	t.Helper()
	cfg := &collage.Config{
		DevMode:  dev,
		BaseURL:  "https://main.example",
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/o.html": {Data: []byte(`<p>{{.}}</p>`)}}, Root: "t"},
		Cache:    collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Minute},
		Plugins:  []collage.Plugin{p},
	}
	if logs != nil {
		cfg.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	app, err := collage.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	content := collage.NewFragment("o", "o.html").
		WithDataHandler(collage.DataHandler(func(_ context.Context, rc *collage.RenderContext) (string, []string, error) {
			return collage.BaseURL(rc), nil, nil
		})).Build()
	if err := app.RegisterPage(collage.NewPage("home").WithContent(content).WithPath("en", "/").Incremental(time.Minute).Build()); err != nil {
		t.Fatal(err)
	}
	doc := collage.NewDocument("o", "text/plain").WithPath("en", "/o.txt").
		WithHandler(func(_ context.Context, rc *collage.RenderContext) ([]byte, []string, error) {
			return []byte("origin=" + collage.BaseURL(rc)), nil, nil
		}).Incremental(time.Minute).Build()
	if err := app.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	return app
}

func getAt(h http.Handler, url string) string {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	return rec.Body.String()
}

// Each host gets its own origin, on a cacheable page and a cacheable document,
// first render and cache hit alike; an unknown host gets Config.BaseURL.
func TestBaseURL_FollowsTheHost(t *testing.T) {
	app := originSite(t, false, nil, &hostOrigins{origins: map[string]string{
		"acme.test":   "https://acme.example/",
		"globex.test": "https://globex.example",
	}})
	h := app.Handler()
	for range 2 { // the second round is served from the cache
		for url, want := range map[string]string{
			"http://acme.test/":        "<p>https://acme.example</p>",
			"http://globex.test/":      "<p>https://globex.example</p>",
			"http://other.test/":       "<p>https://main.example</p>",
			"http://acme.test/o.txt":   "origin=https://acme.example",
			"http://globex.test/o.txt": "origin=https://globex.example",
		} {
			if got := getAt(h, url); !strings.Contains(got, want) {
				t.Errorf("GET %s = %q, want it to contain %q", url, got, want)
			}
		}
	}
}

func TestOriginFor_FoldsHostCase(t *testing.T) {
	app := originSite(t, false, nil, &hostOrigins{origins: map[string]string{"acme.test": "https://acme.example"}})
	for _, host := range []string{"ACME.test", "ACME.test:8080"} {
		if got := app.OriginFor(context.Background(), host); got != "https://acme.example" {
			t.Errorf("OriginFor(%s) = %q, want https://acme.example", host, got)
		}
	}
}

// An IPv6 literal reaches a resolver in one spelling, without brackets, whether
// or not the host had a port.
func TestOriginFor_IPv6WithoutBrackets(t *testing.T) {
	app := originSite(t, false, nil, &hostOrigins{origins: map[string]string{"::1": "https://six.example"}})
	for _, host := range []string{"[::1]", "[::1]:80"} {
		if got := app.OriginFor(context.Background(), host); got != "https://six.example" {
			t.Errorf("OriginFor(%s) = %q, want https://six.example", host, got)
		}
	}
}

// A static build has no request: the origin is Config.BaseURL, even when a
// resolver knows the synthetic request's host, Server.Host.
func TestBaseURL_StaticRenderUsesConfig(t *testing.T) {
	app := originSite(t, false, nil, &hostOrigins{origins: map[string]string{"localhost": "https://tenant.example"}})
	result, err := app.RenderPath(context.Background(), "/", "en", nil)
	if err != nil {
		t.Fatal(err)
	}
	if html := string(result.HTML); !strings.Contains(html, "https://main.example") || strings.Contains(html, "tenant.example") {
		t.Errorf("static render = %q, want Config.BaseURL", html)
	}
	doc, err := app.RenderDocumentPath(context.Background(), "/o.txt", "en", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(doc.Body); got != "origin=https://main.example" {
		t.Errorf("static document = %q, want origin=https://main.example", got)
	}
}

// A fragment a plugin renders outside ServeHTTP follows the request's host too.
func TestBaseURL_RenderFragmentFollowsTheHost(t *testing.T) {
	app := originSite(t, false, nil, &hostOrigins{origins: map[string]string{"acme.test": "https://acme.example"}})
	frag := collage.NewFragment("live", "o.html").
		WithDataHandler(collage.DataHandler(func(_ context.Context, rc *collage.RenderContext) (string, []string, error) {
			return collage.BaseURL(rc), nil, nil
		})).Build()
	if err := app.RegisterPage(collage.NewPage("live").WithContent(frag).WithPath("en", "/live").
		WithFragmentPath("en", "/live/o", frag).Build()); err != nil {
		t.Fatal(err)
	}
	app.Handler()
	got, err := app.RenderFragment(httptest.NewRequest(http.MethodGet, "http://acme.test/stream", nil),
		collage.FragmentRequest{Page: "live", Fragment: "live"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.HTML) != "<p>https://acme.example</p>" {
		t.Errorf("RenderFragment = %q, want the host's origin", got.HTML)
	}
}

// The Host a plugin receives offers Origins, and says origins are dynamic.
func TestOrigins_Capability(t *testing.T) {
	p := &hostOrigins{origins: map[string]string{"acme.test": "https://acme.example"}}
	originSite(t, false, nil, p).Handler() // Init runs when the handler is built
	if !p.sawOrigins || !p.dynamic || p.initOriginOf != "https://acme.example" {
		t.Errorf("Init saw Origins=%v Dynamic=%v OriginFor=%q; want true, true, https://acme.example",
			p.sawOrigins, p.dynamic, p.initOriginOf)
	}
}

// An invalid origin is ignored, so the host gets Config.BaseURL, and development
// says so once.
func TestBaseURL_InvalidOriginIsIgnoredAndLogged(t *testing.T) {
	var logs bytes.Buffer
	app := originSite(t, true, &logs, &hostOrigins{origins: map[string]string{"bad.test": "ftp://bad"}})
	h := app.Handler()
	for range 2 {
		if got := getAt(h, "http://bad.test/"); !strings.Contains(got, "https://main.example") {
			t.Errorf("GET bad.test = %q, want Config.BaseURL", got)
		}
	}
	if n := strings.Count(logs.String(), "invalid origin"); n != 1 {
		t.Errorf("logged %d times, want once: %s", n, logs.String())
	}
}

// An origin with no host name, "https://:8080", is not a bare origin: it is
// ignored and logged like any other invalid one.
func TestBaseURL_OriginWithoutHostnameIsIgnored(t *testing.T) {
	var logs bytes.Buffer
	app := originSite(t, true, &logs, &hostOrigins{origins: map[string]string{"bad.test": "https://:8080"}})
	if got := getAt(app.Handler(), "http://bad.test/"); !strings.Contains(got, "https://main.example") {
		t.Errorf("GET bad.test = %q, want Config.BaseURL", got)
	}
	if n := strings.Count(logs.String(), "invalid origin"); n != 1 {
		t.Errorf("logged %d times, want once: %s", n, logs.String())
	}
}
